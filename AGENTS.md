# Project instructions

## Product boundary

- Package the official Mihomo core as a single-container service.
- Publish only mixed proxy port 7890 and controller/ExternalUI port 9090.
- Read the subscription URL only from `/run/secrets/subscription`.
- Do not add transparent gateway, TUN, firewall, policy-routing, or DNS-hijack behavior.
- Use only the image-packaged ACL4SSR rules and ExternalUI assets at runtime.

## Engineering rules

- Pin release versions and verify every downloaded artifact with SHA-256.
- Preserve `/data/last-good` across restarts and atomically alternate its two managed slots.
- Never log or commit subscription URLs, tokens, or node credentials.
- Add tests before behavior changes and keep Go unit coverage at or above 65%.
- Run `./scripts/test.sh` and `./tests/container-smoke.sh` before publishing.

## Licensing

- Do not commit Mihomo or ExternalUI binaries/assets to this repository.
- The Dockerfile may link to official release URLs and users build the image for their own deployment.
- Packaged Mihomo, MetaCubeXD, and ACL4SSR files retain their upstream licenses.
