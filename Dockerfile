# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.24
ARG ALPINE_VERSION=3.22

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS bootstrap-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/bootstrap ./cmd/bootstrap

FROM alpine:${ALPINE_VERSION} AS release-assets
ARG TARGETARCH
ARG SSCLASH_VERSION=v6.1.0
ARG MIHOMO_VERSION=v1.19.30
ARG MIHOMO_SHA256_AMD64=cf06ce2c7d1421bdbda14ee4a5b6046672dc35ebf8eecd8e77504ec3c0ed9a84
ARG MIHOMO_SHA256_ARM64=58896873736d28628f66de3677c8654fa0f180662523148e136cff4f6e890069
WORKDIR /assets
RUN apk add --no-cache ca-certificates curl gzip
RUN case "${TARGETARCH}" in \
      amd64|arm64) ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}; supported: amd64, arm64" >&2; exit 1 ;; \
    esac; \
    curl --fail --show-error --silent --location --retry 3 \
      --output sha256sums.txt \
      "https://github.com/zerolabnet/SSClash-Go/releases/download/${SSCLASH_VERSION}/sha256sums.txt"; \
    curl --fail --show-error --silent --location --retry 3 \
      --output ssclash \
      "https://github.com/zerolabnet/SSClash-Go/releases/download/${SSCLASH_VERSION}/ssclash-linux-${TARGETARCH}"; \
    expected="$(awk -v asset="ssclash-linux-${TARGETARCH}" '$2 == asset { print $1 }' sha256sums.txt)"; \
    test -n "${expected}"; \
    printf '%s  %s\n' "${expected}" ssclash | sha256sum -c -; \
    chmod 0755 ssclash
RUN case "${TARGETARCH}" in \
      amd64) mihomo_sha256="${MIHOMO_SHA256_AMD64}" ;; \
      arm64) mihomo_sha256="${MIHOMO_SHA256_ARM64}" ;; \
    esac; \
    asset="mihomo-linux-${TARGETARCH}-${MIHOMO_VERSION}.gz"; \
    curl --fail --show-error --silent --location --retry 3 \
      --output mihomo.gz \
      "https://github.com/MetaCubeX/mihomo/releases/download/${MIHOMO_VERSION}/${asset}"; \
    printf '%s  %s\n' "${mihomo_sha256}" mihomo.gz | sha256sum -c -; \
    gzip -d mihomo.gz; \
    chmod 0755 mihomo

FROM alpine:${ALPINE_VERSION} AS acl4ssr-assets
ARG ACL4SSR_REF=6e27259b8625e360699c014f98f978ee7408c644
ARG ACL4SSR_SHA256=72229e2f0a38fc9776720a20dd4ecb44fdd0b0704bbf1f5141732562a237bff2
RUN apk add --no-cache ca-certificates curl
RUN curl --fail --show-error --silent --location --retry 3 \
      --output /tmp/acl4ssr.tar.gz \
      "https://github.com/ACL4SSR/ACL4SSR/archive/${ACL4SSR_REF}.tar.gz"; \
    printf '%s  %s\n' "${ACL4SSR_SHA256}" /tmp/acl4ssr.tar.gz | sha256sum -c -; \
    mkdir -p /out/rules; \
    tar -xzf /tmp/acl4ssr.tar.gz -C /out/rules --strip-components=3 \
      "ACL4SSR-${ACL4SSR_REF}/Clash/Providers"; \
    tar -xOzf /tmp/acl4ssr.tar.gz "ACL4SSR-${ACL4SSR_REF}/LICENCE" \
      > /out/ACL4SSR-LICENSE

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates curl gzip tzdata \
    && addgroup -S ssclash \
    && adduser -S -G ssclash -h /opt/clash ssclash \
    && mkdir -p /opt/clash /tmp/ssclash /usr/local/lib/ssclash /usr/local/share/ssclash \
    && chown -R ssclash:ssclash /opt/clash /tmp/ssclash
COPY --from=bootstrap-builder /out/bootstrap /usr/local/bin/bootstrap
COPY --from=release-assets /assets/ssclash /usr/local/bin/ssclash
COPY --from=release-assets /assets/mihomo /usr/local/lib/ssclash/clash
COPY --from=acl4ssr-assets /out/rules /usr/local/share/ssclash/rules
COPY --from=acl4ssr-assets /out/ACL4SSR-LICENSE /usr/local/share/licenses/ACL4SSR-LICENSE
COPY config/config.yaml /usr/local/share/ssclash/config.yaml

ENV SSCLASH_ROOT=/opt/clash \
    SSCLASH_TMP=/tmp/ssclash \
    SSCLASH_PLATFORM=linux \
    SSCLASH_ADDR=0.0.0.0:9091 \
    SAFE_PATHS=/usr/local/share/ssclash

USER ssclash
VOLUME ["/opt/clash"]
EXPOSE 9091/tcp 7890/tcp 7890/udp
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=4 \
    CMD curl --fail --silent --show-error http://127.0.0.1:9090/version >/dev/null
ENTRYPOINT ["/usr/local/bin/bootstrap"]
