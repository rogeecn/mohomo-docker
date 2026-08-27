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

The host file should remain mode `0600`; Compose exposes it inside the container as a read-only secret. Do not put the URL in `.env`, command-line arguments, Compose YAML, or support logs. The checked-in `.gitignore` and `.dockerignore` exclude the default secret filename, but operators remain responsible for protecting custom secret paths.

Open `http://127.0.0.1:9090/ui/` for the packaged MetaCubeXD interface. It uses Mihomo's controller and proxy-group APIs to inspect status and switch nodes. Proxy clients use:

```text
HTTP proxy:   http://127.0.0.1:7890
SOCKS5 proxy: socks5://127.0.0.1:7890
Controller:   http://127.0.0.1:9090
```

Both ports bind to host loopback by default. `PROXY_BIND`, `PROXY_PORT`, `CONTROLLER_BIND`, and `CONTROLLER_PORT` are optional overrides. Expose port 9090 only to a trusted network or authenticated reverse proxy; this minimal deployment intentionally does not add a second authentication layer.

In MetaCubeXD, open the `🚀 节点选择` group and choose a node or policy. The choice applies to the current Mihomo process only; `profile.store-selected` is disabled, so a restart returns to the configured default.

## Data and restart recovery

Compose mounts the `mihomo-data` named volume at `/data`. It contains generated configuration and normalized subscription data, including node credentials, with private container-side permissions. Treat the volume as sensitive: do not copy it into images, source control, unencrypted backups, or support bundles.

`/data/last-good` is a managed relative symlink to `/data/generations/a` or `/data/generations/b`. Keep the same Compose project and named volume across upgrades and restarts. Do not use `docker compose down --volumes` unless intentionally deleting the cached configuration; removing the volume makes the next start a cold start that requires the subscription endpoint to be reachable.

Normal recovery uses the existing volume:

```sh
docker compose restart mihomo
docker compose ps
```

## Lifecycle and updates

On a fresh volume, bootstrap downloads, normalizes, generates, and validates a candidate with the packaged Mihomo binary before starting Mihomo. This is the cold-start update; a failure exits nonzero without starting an empty configuration.

On restart, bootstrap validates and starts the cached `last-good` slot, waits for the controller, and then immediately attempts an update. After startup processing finishes—including the cold-start update on a fresh volume or the immediate update on a restart—the hourly timer starts. The first scheduled update therefore runs one hour after that processing completes. Each candidate is written to the inactive generation, validated, and atomically selected before Mihomo reloads it through the native `PUT /configs` API.

Download, HTTP, YAML, generation, or Mihomo validation failures reject the candidate and keep the running `last-good`. A reload failure restores the prior pointer and reloads the prior configuration. A storage failure, or failure to persist/reload that rollback, stops Mihomo instead of claiming an unsafe recovery; Compose's restart policy then retries startup from whatever valid `last-good` remains.

Trigger the same update path immediately for tests or operations:

```sh
docker compose kill --signal HUP mihomo
```

The `bootstrap candidate` subcommand remains available for an isolated one-shot candidate pipeline check; a running service should use `SIGHUP` so the result is hot-reloaded.

## Troubleshooting

Start with service state and bounded logs:

```sh
docker compose ps
docker compose logs --tail=200 mihomo
curl --fail http://127.0.0.1:9090/version
docker compose exec mihomo readlink /data/last-good
docker compose exec mihomo /usr/local/bin/mihomo -t -d /data/last-good -f /data/last-good/config.yaml
```

Do not print `/run/secrets/subscription` or `/data/last-good/subscription.yaml` while collecting diagnostics.

- `cold-start candidate failed`: there is no valid cache and the secret, endpoint, response, or generated configuration was rejected. Confirm the secret file contains one reachable absolute HTTP(S) URL and that the response is a single Mihomo/Clash YAML document with a non-empty `proxies` list.
- `update rejected; keeping last-good`: the service remains available on the old configuration. Fix the subscription response or connectivity, then send `SIGHUP` to retry.
- `reload rejected; restored and reloaded last-good`: the new candidate did not load, and bootstrap restored the previous configuration. Inspect the preceding error without exposing the subscription.
- `fatal update stopped Mihomo`: persistence or rollback could not be guaranteed. Check free space, ownership, and write access for the `/data` volume before relying on automatic restart.
- Controller works but the UI does not: use the trailing-slash URL `/ui/` and confirm the 9090 mapping with `docker compose port mihomo 9090`. A remote browser cannot use the default loopback binding; change `CONTROLLER_BIND` only after adding an appropriate network boundary.

## Runtime assets

The image pins and SHA-256 verifies Mihomo `v1.19.30`, MetaCubeXD `v1.273.0`, and ACL4SSR commit `6e27259b8625e360699c014f98f978ee7408c644`. Rules and UI files are local to the image; runtime does not call an online converter or rule provider.

The container runs as an unprivileged user with all capabilities dropped, a read-only root filesystem, and only `/data` writable. Do not publish a derivative image without respecting the upstream Mihomo, MetaCubeXD, and ACL4SSR licenses.

## CI image publishing

GitHub Actions publishes to `ghcr.io/<github.repository>`. Gitea Actions publishes the same tested image to `git.ipao.vip/rogee/mohomo-docker` with `latest` and `sha-<full-commit>` tags. The workflows are independent and do not share registry credentials or provider contexts.

Before enabling Gitea publishing, add a repository Actions secret named `REGISTRY_TOKEN`. It must be a Gitea token whose owner can push packages for `rogee` and link the resulting container package to `rogee/mohomo-docker`. Keep the token out of files and logs, and rotate it in Gitea without changing the workflow.

Gitea publishes only after tests and the container smoke test pass on a `main` push or manual workflow dispatch. Pull requests run those validations but skip secret use, registry login, image push, and package linking. A failed link check leaves the pushed image intact; fix the token permissions and rerun the workflow to retry the idempotent link step.

## Local acceptance

```sh
./tests/container-smoke.sh
```

This single command builds the image and uses an isolated local provider with sanitized fake nodes and a fake query token. It covers cold-start failure, warm recovery, immediate and `SIGHUP` updates, invalid-candidate retention, restart recovery, the 7890/9090 boundary, ExternalUI loading, proxy-group reads, and one node switch. It never uses a real subscription or an online rule conversion service. The image build may still need network access to download the pinned official artifacts whose SHA-256 values are verified.

For the complete developer check set, run:

```sh
./scripts/test.sh
./tests/container-smoke.sh
go vet ./...
go mod verify
git diff --check
```

All test fixtures use only sanitized fake values.
