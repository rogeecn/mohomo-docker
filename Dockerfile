# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.24
ARG ALPINE_VERSION=3.22

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS bootstrap-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/bootstrap ./cmd/bootstrap

FROM alpine:${ALPINE_VERSION} AS mihomo-assets
ARG TARGETARCH
ARG MIHOMO_VERSION=v1.19.30
ARG MIHOMO_SHA256_AMD64=cbe553d0319a414bd3a372c5976a252155b2c4882b66bce88a4d6bba9571a553
ARG MIHOMO_SHA256_ARM64=58896873736d28628f66de3677c8654fa0f180662523148e136cff4f6e890069
RUN apk add --no-cache ca-certificates curl gzip
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) asset="mihomo-linux-amd64-v1-${MIHOMO_VERSION}.gz"; checksum="${MIHOMO_SHA256_AMD64}" ;; \
      arm64) asset="mihomo-linux-arm64-${MIHOMO_VERSION}.gz"; checksum="${MIHOMO_SHA256_ARM64}" ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}; supported: amd64, arm64" >&2; exit 1 ;; \
    esac; \
    curl --fail --show-error --silent --location --retry 3 \
      --output /mihomo.gz "https://github.com/MetaCubeX/mihomo/releases/download/${MIHOMO_VERSION}/${asset}"; \
    printf '%s  %s\n' "${checksum}" /mihomo.gz | sha256sum -c -; \
    gzip -d /mihomo.gz; \
    chmod 0755 /mihomo

FROM alpine:${ALPINE_VERSION} AS acl4ssr-assets
ARG ACL4SSR_REF=6e27259b8625e360699c014f98f978ee7408c644
ARG ACL4SSR_SHA256=72229e2f0a38fc9776720a20dd4ecb44fdd0b0704bbf1f5141732562a237bff2
RUN apk add --no-cache ca-certificates curl
RUN set -eu; \
    curl --fail --show-error --silent --location --retry 3 \
      --output /tmp/acl4ssr.tar.gz "https://github.com/ACL4SSR/ACL4SSR/archive/${ACL4SSR_REF}.tar.gz"; \
    printf '%s  %s\n' "${ACL4SSR_SHA256}" /tmp/acl4ssr.tar.gz | sha256sum -c -; \
    mkdir -p /out/rules; \
    tar -xzf /tmp/acl4ssr.tar.gz -C /out/rules --strip-components=3 \
      "ACL4SSR-${ACL4SSR_REF}/Clash/Providers"; \
    tar -xOzf /tmp/acl4ssr.tar.gz "ACL4SSR-${ACL4SSR_REF}/LICENCE" > /out/ACL4SSR-LICENSE

FROM alpine:${ALPINE_VERSION} AS external-ui-assets
ARG EXTERNAL_UI_VERSION=v1.273.0
ARG EXTERNAL_UI_SHA256=076e05d2e3dc6641a0ec281aa4b97a18193fbcc379d139762c32d90adb22793c
ARG EXTERNAL_UI_LICENSE_SHA256=cd0735ba06f26a0008bbca399890c7ca87fe129aacc302c2e33fb03e60a4e8c3
RUN apk add --no-cache ca-certificates curl
RUN set -eu; \
    curl --fail --show-error --silent --location --retry 3 \
      --output /tmp/ui.tgz "https://github.com/MetaCubeX/metacubexd/releases/download/${EXTERNAL_UI_VERSION}/compressed-dist.tgz"; \
    printf '%s  %s\n' "${EXTERNAL_UI_SHA256}" /tmp/ui.tgz | sha256sum -c -; \
    mkdir -p /out/ui; \
    tar -xzf /tmp/ui.tgz -C /out/ui; \
    curl --fail --show-error --silent --location --retry 3 \
      --output /out/METACUBEXD-LICENSE "https://raw.githubusercontent.com/MetaCubeX/metacubexd/${EXTERNAL_UI_VERSION}/LICENSE"; \
    printf '%s  %s\n' "${EXTERNAL_UI_LICENSE_SHA256}" /out/METACUBEXD-LICENSE | sha256sum -c -

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates curl tzdata \
    && addgroup -S mihomo \
    && adduser -S -G mihomo -h /data mihomo \
    && mkdir -p /data /run/secrets /usr/local/share/mihomo \
    && chown mihomo:mihomo /data
COPY --from=bootstrap-builder /out/bootstrap /usr/local/bin/bootstrap
COPY --from=mihomo-assets /mihomo /usr/local/bin/mihomo
COPY --from=acl4ssr-assets /out/rules /usr/local/share/mihomo/rules
COPY --from=acl4ssr-assets /out/ACL4SSR-LICENSE /usr/local/share/licenses/ACL4SSR-LICENSE
COPY --from=external-ui-assets /out/ui /usr/local/share/mihomo/ui
COPY --from=external-ui-assets /out/METACUBEXD-LICENSE /usr/local/share/licenses/METACUBEXD-LICENSE
COPY config/config.yaml /usr/local/share/mihomo/config.yaml

ENV SAFE_PATHS=/usr/local/share/mihomo:/data

USER mihomo
VOLUME ["/data"]
EXPOSE 7890/tcp 9090/tcp
HEALTHCHECK --interval=15s --timeout=5s --start-period=15s --retries=4 \
    CMD curl --fail --silent --show-error http://127.0.0.1:9090/version >/dev/null
ENTRYPOINT ["/usr/local/bin/bootstrap"]
