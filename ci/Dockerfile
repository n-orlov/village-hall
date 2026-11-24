# board's CI toolchain image: Go on the 1.26.x line, Node 24, the Playwright
# browsers and the quality-gate tools. Every FROM and every COPY --from image
# is pinned by digest (a tag can be re-pushed); the tag stays in the comment
# above the line, so a bump changes both. Bump a version here and nowhere else.

# golang:1.26.4-trixie
FROM golang:1.26.4-trixie@sha256:68b7145ec43d1820b9a56704554b53d1520aa2a15cb5233e374188a31b2a1bce AS tools
RUN GOBIN=/out go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 \
    && GOBIN=/out go install golang.org/x/vuln/cmd/govulncheck@v1.8.0 \
    && GOBIN=/out go install gotest.tools/gotestsum@v1.13.0 \
    && GOBIN=/out go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# node:24-trixie-slim (Node 24.22.2)
FROM node:24-trixie-slim@sha256:0820eaa85e07d0490446dd01d14707e17e6108a36988cf374899cd057eb3812d AS node

# golang:1.26.11-trixie
FROM golang:1.26.11-trixie@sha256:fa340cb59ebeb1d53ac1cccfc334201d938fe5f3458003cccb767096d8e032b8

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        shellcheck \
    && rm -rf /var/lib/apt/lists/*

COPY --from=tools /out/golangci-lint /out/govulncheck /out/gotestsum /out/gremlins /usr/local/bin/

# Node 24 from the official image: the binary and npm/corepack's tree.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && ln -s ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack

# Playwright 1.64.1 Chromium (and its system libraries; no Firefox, no WebKit: every project and every web/ test uses
# chromium) in a fixed path every
# user can read. The playwright package used here only installs the browsers:
# web/ and e2e/ pin the same version in their own package.json.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN mkdir -p /tmp/pw && cd /tmp/pw \
    && npm init -y >/dev/null \
    && npm install --no-audit --no-fund playwright@1.64.1 \
    && npx playwright install --with-deps chromium \
    && chmod -R a+rX /ms-playwright \
    && rm -rf /tmp/pw /root/.npm /var/lib/apt/lists/*

# Trivy 0.72.0, pinned by digest.
COPY --from=public.ecr.aws/aquasecurity/trivy@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f /usr/local/bin/trivy /usr/local/bin/trivy

ENV GOMODCACHE=/go-cache/mod \
    GOCACHE=/go-cache/build \
    GOLANGCI_LINT_CACHE=/go-cache/golangci \
    npm_config_cache=/npm-cache \
    HOME=/tmp

RUN mkdir -p "$GOMODCACHE" "$GOCACHE" "$GOLANGCI_LINT_CACHE" "$npm_config_cache" \
    && chown -R 1000:1000 /go-cache /npm-cache

# Non-root by default (Trivy DS-0002). ci/run.sh still passes --user uid:gid.
USER 1000:1000

VOLUME ["/go-cache", "/npm-cache"]
WORKDIR /w
