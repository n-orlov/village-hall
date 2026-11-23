#!/bin/sh
# ci/oci-name.sh -- copies an OCI layout tar and names its image, changing nothing
# else: only index.json gains the name annotations buildx writes for
# `--output type=oci,name=<ref>` (io.containerd.image.name and
# org.opencontainers.image.ref.name); every blob, so every image digest, is
# untouched. ci/release-build.sh uses it to name the image the Image unit already
# built village-hall:<version>, so the release dry run builds no image of its own.
# An image that already carries the name is copied as it is. Runs inside ci/run.sh
# (tar and sed only).
#
#   ci/oci-name.sh <in.tar> <out.tar> <name:tag>
set -eu

die() {
	echo "ci/oci-name.sh: $*" >&2
	exit 1
}

[ $# -eq 3 ] || die "usage: ci/oci-name.sh <in.tar> <out.tar> <name:tag>"
in=$1
out=$2
ref=$3
tag=${ref##*:}
if [ "$tag" = "$ref" ] || [ -z "$tag" ]; then
	die "$ref is not name:tag"
fi
[ -s "$in" ] || die "$in is missing or empty"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
tar -C "$work" -xf "$in"
[ -s "$work/index.json" ] || die "$in holds no index.json"

names="\"io.containerd.image.name\":\"docker.io/library/$ref\",\"org.opencontainers.image.ref.name\":\"$tag\""
if grep -q '"io.containerd.image.name"' "$work/index.json"; then
	cp "$work/index.json" "$work/index.json.new"
elif grep -q '"annotations":{' "$work/index.json"; then
	sed "s|\"annotations\":{|&$names,|" "$work/index.json" > "$work/index.json.new"
else
	sed "s|\"size\":\([0-9]*\)}|\"size\":\1,\"annotations\":{$names}}|" "$work/index.json" > "$work/index.json.new"
fi
grep -q "\"io.containerd.image.name\":\"[^\"]*$ref\"" "$work/index.json.new" || die "could not name the image $ref in $in's index.json"
mv "$work/index.json.new" "$work/index.json"

mkdir -p "$(dirname "$out")"
rm -f "$out"
# shellcheck disable=SC2046 # the layout's top-level names hold no spaces
tar -C "$work" -cf "$out" $(cd "$work" && ls -A)
