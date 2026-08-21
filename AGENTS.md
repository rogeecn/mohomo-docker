# Project instructions

## Product boundary

- Package the official SSClash-Go release binary with an official Mihomo core.
- Run SSClash in `server` mode only: embedded Web UI plus Mihomo mixed proxy on port 7890.
- Keep both `OPERATING_MODE=server` and `PROXY_MODE=none`; without the latter, SSClash injects a gateway listener during Web-managed start.
- Do not add transparent gateway, TUN, firewall, policy-routing, or DNS-hijack behavior.
- Keep Mihomo's controller private to the container; never publish port 9090.

## Engineering rules

- Pin release versions and verify every downloaded artifact with SHA-256.
- Preserve user-managed files in `/opt/clash`; initialization may only create missing files.
- Fail explicitly on corrupt or ambiguous persistent state.
- Add tests before behavior changes and keep Go unit coverage at or above 65%.
- Run `./scripts/test.sh` and `./tests/container-smoke.sh` before publishing.
- Keep startup logs sufficient to identify initialization, selected mode, and executed command.

## Licensing

- Do not commit SSClash or Mihomo binaries to this repository.
- The Dockerfile may link to official release URLs and users build the image for their own deployment.
- Do not publish a prebuilt image containing SSClash without permission from its copyright holder.
