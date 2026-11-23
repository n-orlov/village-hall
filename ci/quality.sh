#!/bin/sh
# ci/quality.sh -- R187's one quality-gate entry point. It scrubs every
# `BOARD_*` variable from its own environment (the same leak this repo's
# other scripts guard against -- see ci/suite.sh's own header), then runs
# `go run ./ci/qualitycheck` against the single checked-in thresholds
# file, ci/quality.json, and the merged coverage profile ci/suite.sh
# produces. ci/qualitycheck does the actual gate work (reading
# thresholds, running whichever gates are switched on, formatting one
# report section per on gate); this script's only job is environment
# hygiene and resolving the two paths qualitycheck needs.
#
#   ci/run.sh ci/quality.sh                  # suite-outdir defaults to ci-results
#   ci/run.sh ci/quality.sh ci-results        # same, explicit
#   ci/run.sh ci/quality.sh /tmp/out my.json  # a test's own outdir + config
#
# Every SPEC 15.4 gate is on in ci/quality.json from the first commit. A
# gate whose input does not exist yet (the frontend, the image, the OpenAPI
# document) fails instead of passing, so this script exits non-zero until
# those inputs land. The frontend gates read what ci/suite.sh (web-coverage/)
# and ci/mutation.sh (mutation/stryker/) wrote under "$outdir". The golangci gate runs ci/golangci.sh, the same single
# invocation ci/lint.sh runs. The SBOM gates write CycloneDX files into
# "$outdir". With every gate off this script still runs, prints that
# nothing is enabled, and exits 0.
set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

# outdir: the directory ci/suite.sh wrote its merged coverage profile
# into (coverage-merged.out). Positional, not BOARD_CI_OUT, so a caller
# never needs a BOARD_* variable just to point this script somewhere --
# consistent with task 002's own outdir argument on ci/suite.sh.
outdir=${1:-ci-results}

# config: the thresholds file. Positional and optional so a test can
# point this script at its own seeded copy without touching the
# checked-in ci/quality.json (which never changes for a test run).
config=${2:-ci/quality.json}

# mutation: "--mutation-deferred" as the third argument reports the two
# mutation gates (mutation-go, stryker) as judged by the Jenkinsfile's
# Mutation stage instead of reading ci-results/mutation/, which that later
# stage writes and a branch build never writes. ci/mutation.sh judges the same
# thresholds from ci/quality.json. Without the argument they are read here, and
# a missing report fails.
mutation_flag=${3:-}
case "$mutation_flag" in
    ""|--mutation-deferred) ;;
    *) echo "usage: ci/quality.sh [outdir [config [--mutation-deferred]]]" >&2; exit 2 ;;
esac

# Scrub leaked BOARD_* environment (R187: "Gate and suite scripts scrub
# every BOARD_* variable"), identical in mechanism to ci/suite.sh's own
# scrub -- a stray BOARD_GODOG_PATHS/BOARD_HOME from the caller's shell
# must never reach `go run ./ci/qualitycheck` or anything it spawns
# (ci/crapgate, in turn, via `go run ./ci/crapgate`).
for board_var in $(env | sed -n -e 's/^\(BOARD_[A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' -e 's/^\(VH_[A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' | sort -u); do
    unset "$board_var"
done

profile="$outdir/coverage-merged.out"

# Trivy's DB cache lives under the image's /go-cache volume so a warm run
# is fast (R190); outside the image (no /go-cache) it falls back to a
# temp dir. TRIVY_CACHE_DIR overrides both.
if [ -n "${TRIVY_CACHE_DIR:-}" ]; then
    trivy_cache=$TRIVY_CACHE_DIR
elif [ -d /go-cache ] && [ -w /go-cache ]; then
    trivy_cache=/go-cache/trivy
else
    trivy_cache=${TMPDIR:-/tmp}/board-trivy-cache
fi

# govulncheck's vuln-DB cache goes to the same volume (it follows
# XDG_CACHE_HOME); outside the image it inherits the caller's own cache.
govuln_cache=
if [ -d /go-cache ] && [ -w /go-cache ]; then
    govuln_cache=/go-cache/xdg-cache
    mkdir -p "$govuln_cache"
fi

# R187: the gate report is also written to "$outdir/quality-report.txt", so
# it crosses the ci/run.sh sibling boundary on the bind-mounted workspace and
# the Jenkinsfile's report (ci/jenkins-report.sh) can put it on the build's
# "CI summary" page whether the gates pass or fail. stdout (what a local
# run prints) is the same text; only stdout is captured, so go run's and
# qualitycheck's stderr (usage/config errors) stays on stderr.
report_file="$outdir/quality-report.txt"
mkdir -p "$outdir"
rm -f "$report_file"

exit_code=0
set -- -config "$config" -profile "$profile"
[ -z "$mutation_flag" ] || set -- "$@" -mutation-deferred
# -unit-results: a unit that failed or was cut leaves unit-failure-<unit>.txt and
# partial JUnit files in "$outdir"; the report's units section names them.
set -- "$@" -unit-results "$outdir"
go run ./ci/qualitycheck "$@" \
    -trivy-target . -trivy-cache "$trivy_cache" -trivy-ignore .trivyignore \
    -govulncheck-target . -govulncheck-cache "$govuln_cache" \
    -sbom-outdir "$outdir" \
    > "$report_file" || exit_code=$?

# A run that died before printing any gate section (bad config, build
# failure) still leaves a report saying so, never an empty or missing file.
if [ ! -s "$report_file" ]; then
    echo "ci/quality.sh: no gate report was produced (exit status $exit_code); see the step log for the error." > "$report_file"
fi

cat "$report_file"
exit "$exit_code"
