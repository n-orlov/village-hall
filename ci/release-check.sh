#!/bin/sh
# ci/release-check.sh -- the release dry run (SPEC 12.5, 15.3): ci/release-build.sh
# builds every asset of a release at the checked-out commit, nothing is published,
# and ci/repocheck's TestReleaseBuild asserts what came out:
#
#   * the four vh_<os>_<arch>.tar.gz, each holding an executable vh;
#   * checksums.txt, in `<sha256>  <file>` form, covering every asset and verifying;
#   * the linux/amd64 vh printing the version;
#   * every CycloneDX SBOM parsing, one per tarball and one for the image;
#   * the OCI index of the image village-hall listing linux/amd64 and linux/arm64;
#   * install.sh and INSTALL-AGENT.md as committed.
#
# The image is not built again: the dry run needs the board-image-oci.tar and
# board-image.tar the Image unit's ci/image-build.sh left in <dir> (it stops if
# they are missing) and hands that directory to ci/release-build.sh as
# BOARD_RELEASE_IMAGE_DIR, so the image the release check inspects is the one
# ci/image-check.sh just checked: the root Dockerfile is built once per architecture,
# and (with the Web UI unit's stashed build) without a web build.
#
# On the Jenkins agent, outside ci/run.sh for the docker parts, in the Image unit
# after ci/image-check.sh. The assets are large, so they are removed afterwards
# (with the dry-run image tag); what stays in <dir> is the JUnit file
# (junit-merged/junit-release-dryrun.xml) and release-dryrun-checksums.txt.
#
#   ci/release-check.sh [dir]    # default ci-results
set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

dir=${1:-ci-results}
version=v0.0.0-dryrun
out="$dir/release-dryrun"

# shellcheck disable=SC2317 # invoked by the trap below
cleanup() {
	rm -rf "$out"
	docker rmi --force "village-hall:$version" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

for want in board-image-oci.tar board-image.tar; do
	[ -s "$dir/$want" ] || { echo "ci/release-check.sh: $dir/$want is missing: run ci/image-build.sh \"$dir/board-image.tar\" first, the dry run builds no image of its own" >&2; exit 1; }
done

echo "ci/release-check.sh: building $version into $out from the image in $dir"
BOARD_RELEASE_IMAGE_DIR=$dir ci/release-build.sh "$version" "$out"

mkdir -p "$dir/junit-merged"
rm -f "$dir/junit-merged/junit-release-dryrun.xml"
echo "ci/release-check.sh: TestReleaseBuild over $out"
ci/run.sh env BOARD_RELEASE_OUT="/w/$out" BOARD_RELEASE_VERSION="$version" \
	gotestsum --junitfile "$dir/junit-merged/junit-release-dryrun.xml" --format testname -- \
	-tags releasebuild -count=1 -run '^TestReleaseBuild' ./ci/repocheck/
[ -s "$dir/junit-merged/junit-release-dryrun.xml" ] || { echo "ci/release-check.sh: TestReleaseBuild wrote no JUnit file" >&2; exit 1; }
cp "$out/checksums.txt" "$dir/release-dryrun-checksums.txt"
echo "ci/release-check.sh: the dry run built and verified every release asset"
