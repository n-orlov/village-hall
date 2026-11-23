#!/bin/sh
# ci/flakygate.sh <results dir> — fails the build when a test was flaky or the
# database legs disagreed.
#
# The suite runs every test once and keeps no retry, so a flaky list
# (<dir>/flaky-*.txt, one line per test that failed and then passed) or a
# leg-divergence list (<dir>/leg-divergence.txt, one line per scenario that
# gave different answers on the SQLite and Postgres legs) should always be
# empty or absent. A line in any of them is a test that cannot be trusted, and
# the build fails on it rather than passing with a note.
#
# Exit 0: no such list, or only empty ones. Exit 1: at least one line in at
# least one list (each is printed, prefixed with its file name).
set -eu

dir=${1:?"usage: ci/flakygate.sh <results dir>"}
bad=0
for list in "$dir"/flaky-*.txt "$dir"/leg-divergence.txt; do
    [ -f "$list" ] || continue
    if grep -q '[^[:space:]]' "$list"; then
        echo "ci/flakygate.sh: $(basename "$list") is not empty:" >&2
        grep '[^[:space:]]' "$list" | sed 's/^/  /' >&2
        bad=1
    fi
done
if [ "$bad" -ne 0 ]; then
    echo "ci/flakygate.sh: a flaky test or a leg divergence fails the build" >&2
    exit 1
fi
echo "ci/flakygate.sh: no flaky test and no leg divergence in $dir"
