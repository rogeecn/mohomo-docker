# mohomo-docker

Docker packaging for the official SSClash-Go daemon and Mihomo core. It intentionally provides only:

- SSClash embedded Web UI on port `9091`;
- Mihomo HTTP/SOCKS mixed proxy on port `7890`;
- server mode, without TUN, transparent proxy, firewall, routing, or DNS interception.

## Quick start

```sh
cp .env.example .env
docker compose up -d --build
docker compose logs -f ssclash
```

Open `http://<server>:9091`, create the administrator password, review `config.yaml`, and start the proxy from the Web UI. The seeded configuration exposes a direct-only `PROXY` group so port `7890` can be tested before adding a subscription.

Configure clients with either of these endpoints:

```text
HTTP proxy:   http://<server>:7890
SOCKS5 proxy: socks5://<server>:7890
```

The Web UI and proxy listen on all host interfaces by default. Change `WEB_BIND` or `PROXY_BIND` in `.env` to restrict them. Do not expose the Web UI to the Internet without HTTPS and an additional access-control layer. Configure Mihomo proxy authentication before exposing port `7890` outside a trusted network.

## Persistent data

The named volume `ssclash-data` is mounted at `/opt/clash` and stores:

- `config.yaml` and named configurations;
- SSClash settings and administrator credentials;
- subscription, rule-provider, and proxy-provider files;
- the active Mihomo core and its runtime data.

The bootstrap process creates missing files only. Existing Mihomo and configuration files are preserved. It explicitly enforces `OPERATING_MODE=server` and `PROXY_MODE=none`; the latter prevents SSClash from synchronizing gateway-only TProxy, redirect, or TUN listeners into `config.yaml`. Duplicate mode entries or empty runtime files cause startup to fail with a diagnostic message.

Resetting the volume deletes configuration and credentials. Inspect the exact Compose project and volume name before doing so.

## Version updates

Versions are pinned in the Dockerfile:

- SSClash-Go `v6.1.0`;
- Mihomo `v1.19.30`.

SSClash is verified against the checksum file from its official release. Mihomo amd64 and arm64 archives are verified against pinned SHA-256 values. To update either component, update the version and checksums together, then run the complete test suite.

## GitHub Container Registry

The GitHub Actions workflow builds `linux/amd64` only. SSClash-Go and Mihomo are downloaded and checksum-verified during the Docker build, so the resulting container never downloads executable files at startup.

The workflow runs tests before building, publishes to `ghcr.io/<github-owner>/mohomo-docker`, attaches SBOM and provenance, and creates these tags:

- `latest` and `main` from the default branch;
- the Git tag and major/minor tags from releases such as `v1.2.3`;
- an immutable `sha-<commit>` tag;
- pull-request tags for build validation only, without pushing.

Keep the GHCR package visibility **private**. The workflow deliberately does not attempt to change package visibility. Making an image containing SSClash-Go available to third parties conflicts with the upstream binary license unless the copyright holder grants permission.

## Tests

```sh
./scripts/test.sh
./tests/container-smoke.sh
```

The unit suite enforces at least 65% statement coverage for bootstrap behavior. The container smoke test builds the image, validates the Mihomo configuration, starts the Web UI with all Linux capabilities dropped, authenticates to SSClash, starts Mihomo through the Web API, rejects gateway-listener/error regressions, and sends an HTTPS request through the mapped mixed proxy port.

## License boundary

This repository contains only original Docker packaging and bootstrap code. It downloads SSClash-Go and Mihomo from their official releases while building.

SSClash-Go uses a proprietary binary license that permits personal/internal use but prohibits redistributing its binary to third parties. Do not publish the resulting image without the copyright holder's permission. Mihomo is separately licensed under GPL-3.0.
