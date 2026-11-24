# Village Hall container image (SPEC 12.4). Multi-stage, final stage FROM scratch with
# the binary, CA certificates and tzdata, running as a non-root uid. Every
# builder stage runs on the build host's own platform (--platform=$BUILDPLATFORM)
# and cross-compiles with GOOS/GOARCH from the target platform, so the linux/amd64
# and linux/arm64 images are built without emulation: the final stage only
# COPYs, it never RUNs anything. Every FROM is pinned by digest, and
# ci/repocheck (dockerfiledigest_test.go, dockerfile_test.go) fails on one that
# is not, on a final stage that is not scratch or distroless, on a root user and
# on a builder stage without --platform=$BUILDPLATFORM. CI builds both platforms
# on every build and does not push them (ci/image-build.sh, ci/image-check.sh).

# The Svelte UI, embedded into the binary by the next stage.
FROM --platform=$BUILDPLATFORM node:24-trixie-slim@sha256:0820eaa85e07d0490446dd01d14707e17e6108a36988cf374899cd057eb3812d AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# npm run build writes ../internal/webui/dist.
RUN mkdir -p ../internal/webui/dist && npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.11-trixie@sha256:fa340cb59ebeb1d53ac1cccfc334201d938fe5f3458003cccb767096d8e032b8 AS build
ARG TARGETOS
ARG TARGETARCH
# VERSION and REVISION are stamped into the binary (internal/version), so `vh version`
# and GET /api/v1/meta report what the image was built as; the build context has no .git.
ARG VERSION=devel
ARG REVISION=
# GOMAXPROCS bounds the compiler's threads when the builder is given one (ci/image-build.sh
# passes its CPU cap): a BuildKit step cgroup does not show Go the builder's cpu quota, so
# without it each cross-compile starts a thread per hardware thread. Unset: Go's default.
ARG GOMAXPROCS
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/ internal/webui/dist/
# timetzdata embeds the time zone database, so the scratch image needs no
# /usr/share/zoneinfo; there is no .git in the build context, hence -buildvcs=false.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false -tags timetzdata -ldflags="-s -w -X github.com/n-orlov/village-hall/internal/version.version=$VERSION -X github.com/n-orlov/village-hall/internal/version.revision=$REVISION" -o /out/vh ./cmd/vh \
 && mkdir -p /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/vh /vh
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
LABEL org.opencontainers.image.title="Village Hall"
ENV BOARD_LISTEN=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/vh"]
CMD ["serve"]
