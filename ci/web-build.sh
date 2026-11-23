#!/bin/sh
# ci/web-build.sh — builds the web UI once for a CI build (R9 l): `npm ci` and
# `npm run build` in web/, which write internal/webui/dist (the directory the Go
# binary embeds), then a marker file beside the build. The Jenkins "Web UI" unit
# runs this and stashes dist; Suite, E2E and Quality unstash it, and
# ci/suite.sh, finding the marker, runs no `npm run build` of its own.
#
#   ci/run.sh ci/web-build.sh        # from a sandbox without local Node
#   ci/web-build.sh                  # on a runner with Node on PATH already
#
# The marker (internal/webui/dist/.ci-stashed-build) is written only after a
# successful build, and the build itself wipes dist (vite's emptyOutDir), so the
# marker never survives beside a different build. Only this script writes it:
# a checkout that built its UI some other way has none, and ci/suite.sh builds
# there as before, so a stale dist left in a work tree is never taken for the
# stashed one.
set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

(cd web && npm ci --no-audit --no-fund && npm run build)
if [ ! -f internal/webui/dist/index.html ]; then
    echo "ci/web-build.sh: the build wrote no internal/webui/dist/index.html" >&2
    exit 1
fi
: > internal/webui/dist/.ci-stashed-build
echo "ci/web-build.sh: web UI built into internal/webui/dist"
