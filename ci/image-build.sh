#!/bin/sh
# ci/image-build.sh -- builds the container image from the root Dockerfile for
# linux/amd64 and linux/arm64 and keeps three things, none of them pushed:
#
#   <dir>/board-image-oci.tar  both platforms as one OCI layout, which
#                              ci/image-check.sh inspects
#   <dir>/board-image.tar      the amd64 image as `docker save`, which
#                              ci/quality.sh's trivy-image and sbom-image gates scan
#   board:ci                   the amd64 image, loaded into the docker daemon
#
# Both platforms are built on the agent's own platform, without emulation: the
# Dockerfile's builder stages are pinned to --platform=$BUILDPLATFORM and
# cross-compile, and its final stage only COPYs (ci/repocheck enforces both).
# The Jenkins agent's docker has no buildx and no multi-platform builder, so
# this fetches the buildx plugin from a digest-pinned image and runs a
# digest-pinned BuildKit container for the duration of the build. On the
# Jenkins agent, outside ci/run.sh (it needs the docker CLI), in the Quality
# stage.
#
# The web UI is built once per CI build (ci/web-build.sh, the Jenkins Web UI unit).
# When internal/webui/dist holds that build (the marker ci/web-build.sh leaves
# beside it, and an index.html), both builds below pass it to the Dockerfile as the
# named build context `web`, which replaces the Dockerfile's `web` stage: the image
# embeds the stashed UI and no web build runs. Without the marker (a standalone
# run) the Dockerfile builds the UI itself, so a stale dist is never embedded.
#
# BOARD_CI_CPUS=<n> (the Jenkinsfile sets it on the Image unit, as it does for
# ci/run.sh) caps the BuildKit container at n CPUs (cpu-quota over a 100 ms
# period), so the two cross-compiles of the Dockerfile cannot run the agent's
# load past 12 (R6); unset or empty: no cap. It is also passed to the Dockerfile
# as the GOMAXPROCS build arg, because a BuildKit step cgroup hides the quota from
# Go and the two cross-compiles would otherwise run a thread per hardware thread
# each (28 runnable compile threads in #310's first minutes, load 22).
#
#   ci/image-build.sh [amd64-tar]    # default ci-results/board-image.tar
set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

# Pinned by digest, like every image here. Bumping either is this file's only edit.
buildx_image='docker/buildx-bin:0.27.0@sha256:d989e4378c270b8ae1b05ee52c99b9be6a75f1aa06fd5564beac05cddbedb86b'
buildkit_image='moby/buildkit:v0.26.0@sha256:93da6a53287490dd3b35952f73a109654ed9f260f2e6434db925125fa3ac6b2a'

# The image reports these (SPEC 13): BOARD_IMAGE_VERSION is the tag or the short sha
# (the Jenkinsfile sets it), BOARD_IMAGE_REVISION the full commit.
version=${BOARD_IMAGE_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo devel)}
revision=${BOARD_IMAGE_REVISION:-$(git rev-parse HEAD 2>/dev/null || true)}

tar_path=${1:-ci-results/board-image.tar}
oci_path="$(dirname "$tar_path")/board-image-oci.tar"
# BOARD_IMAGE_TAG renames the amd64 image loaded into the daemon, and
# BOARD_IMAGE_NAME names the image in the OCI layout (its ref.name annotation);
# ci/release-build.sh sets both so the release image is village-hall:<version>
# and never replaces board:ci.
tag=${BOARD_IMAGE_TAG:-board:ci}
oci_name=${BOARD_IMAGE_NAME:-}
builder="board-image-$$"
work=$(mktemp -d)
cleanup() {
	docker buildx rm --force "$builder" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

# The buildx CLI plugin, from the pinned image, in a docker config of its own.
# $DOCKER_CONFIG's other contents are not needed: every image here is public.
mkdir -p "$work/config/cli-plugins"
holder=$(docker create "$buildx_image" /buildx)
docker cp "$holder:/buildx" "$work/config/cli-plugins/docker-buildx"
docker rm "$holder" >/dev/null
export DOCKER_CONFIG="$work/config"

cpu_opts=""
procs_arg=""
if [ -n "${BOARD_CI_CPUS:-}" ]; then
	procs_arg="--build-arg GOMAXPROCS=$BOARD_CI_CPUS"
	cpu_opts="--driver-opt cpu-period=100000 --driver-opt cpu-quota=$((BOARD_CI_CPUS * 100000))"
fi
# shellcheck disable=SC2086 # cpu_opts is two words per option, by design
docker buildx create --name "$builder" --driver docker-container \
	--driver-opt "image=$buildkit_image" $cpu_opts >/dev/null

# web_opts is the named build context (two words), or empty; unquoted below on
# purpose (SC2086); an absolute path, because the context directory is named to buildx.
web_opts=""
if [ -f internal/webui/dist/.ci-stashed-build ] && [ -f internal/webui/dist/index.html ]; then
	web_opts="--build-context web=$repo_root/internal/webui/dist"
	echo "ci/image-build.sh: embedding the stashed web UI build (no web build in the image)"
fi

mkdir -p "$(dirname "$tar_path")"
rm -f "$tar_path" "$oci_path"

# procs_arg is one word-pair, or empty, by design (SC2086 below).
# Both platforms in one build: the OCI layout holds an image index with both.
# shellcheck disable=SC2086
docker buildx build --builder "$builder" -f Dockerfile $procs_arg $web_opts \
	--platform linux/amd64,linux/arm64 --provenance=false --sbom=false \
	--build-arg "VERSION=$version" --build-arg "REVISION=$revision" \
	--output "type=oci,dest=$oci_path${oci_name:+,name=$oci_name}" .
# The amd64 image again, from the builder's cache: loaded and saved.
# shellcheck disable=SC2086
docker buildx build --builder "$builder" -f Dockerfile $procs_arg $web_opts \
	--platform linux/amd64 --provenance=false --sbom=false \
	--build-arg "VERSION=$version" --build-arg "REVISION=$revision" \
	--tag "$tag" --load .
docker save -o "$tar_path" "$tag"

[ -s "$oci_path" ] || { echo "ci/image-build.sh: $oci_path is empty" >&2; exit 1; }
[ -s "$tar_path" ] || { echo "ci/image-build.sh: $tar_path is empty" >&2; exit 1; }
echo "ci/image-build.sh: linux/amd64 and linux/arm64 built into $oci_path ($tag loaded, saved to $tar_path)"
