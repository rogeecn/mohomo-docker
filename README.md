# mohomo-docker

Single-container Mihomo service using the repository-packaged ACL4SSR `Online Full MultiMode` routing model. Runtime access is limited to mixed proxy port 7890 and Mihomo controller/ExternalUI port 9090.

## Start

```sh
cp .env.example .env
printf '%s\n' 'https://subscription.example.invalid/mihomo' > subscription.url
chmod 0600 subscription.url
docker compose up -d --build
```

`subscription.url` must contain exactly one absolute HTTP(S) URL without URL userinfo. Compose mounts it read-only at `/run/secrets/subscription`; the URL is never passed in the environment or written to the image, volume, generated configuration, or logs. Keep this file out of Git.

Open `http://127.0.0.1:9090/ui/` for the packaged MetaCubeXD interface. It uses Mihomo's controller and proxy-group APIs to inspect status and switch nodes. Proxy clients use:

```text
HTTP proxy:   http://127.0.0.1:7890
SOCKS5 proxy: socks5://127.0.0.1:7890
Controller:   http://127.0.0.1:9090
```

Both ports bind to host loopback by default. `PROXY_BIND`, `PROXY_PORT`, `CONTROLLER_BIND`, and `CONTROLLER_PORT` are optional overrides. Expose port 9090 only to a trusted network or authenticated reverse proxy; this minimal deployment intentionally does not add a second authentication layer.

## Lifecycle and updates

On a fresh volume, bootstrap downloads, normalizes, generates, and validates a candidate with the packaged Mihomo binary before starting Mihomo. A failure exits nonzero without starting an empty configuration.

`/data/last-good` atomically points to one of two generation slots. On restart, a valid cached slot starts first and bootstrap immediately attempts an update. Download, HTTP, YAML, generation, Mihomo validation, publication, or reload failures leave the previous slot active. Successful updates use Mihomo's native `PUT /configs` API without replacing the foreground process.

Updates run hourly from container start. Trigger the same update path immediately for tests or operations:

```sh
docker kill --signal HUP mohomo-docker
```

The `bootstrap candidate` subcommand remains available for an isolated one-shot candidate pipeline check; a running service should use `SIGHUP` so the result is hot-reloaded.

## Runtime assets

The image pins and SHA-256 verifies Mihomo `v1.19.30`, MetaCubeXD `v1.273.0`, and ACL4SSR commit `6e27259b8625e360699c014f98f978ee7408c644`. Rules and UI files are local to the image; runtime does not call an online converter or rule provider.

The container runs as an unprivileged user with all capabilities dropped, a read-only root filesystem, and only `/data` writable. Do not publish a derivative image without respecting the upstream Mihomo, MetaCubeXD, and ACL4SSR licenses.

## Verification

```sh
./scripts/test.sh
./tests/container-smoke.sh
go vet ./...
go mod verify
git diff --check
```

Tests use only local fake subscription URLs and fake node data.
