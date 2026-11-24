#!/bin/sh
# ci/suite.sh — the CI suite runner (R145): drives the whole test matrix
# (`go test -p=1 -count=1 ./...`: pass 1 is every test function in every
# package except features/TestFeatures, pass 2 is TestFeatures) through
# gotestsum, fetched at a
# pinned version rather than vendored or added to go.mod (`go run
# gotest.tools/gotestsum@$GOTESTSUM_VERSION`), so this file is the only
# place the version is bumped and ci/Dockerfile never needs to know
# gotestsum exists. It writes standard JUnit for the ordinary Go package
# matrix and Godog's own JUnit for features/.
#
# No retries (task 003, R3): Vitest, Playwright, the Go package matrix and
# features/TestFeatures (once per database leg) each run exactly once, with no
# repeat, no gotestsum --rerun-fails and no per-scenario rerun. A failure in any
# of the four makes this script exit non-zero, and a test that fails once is a
# failure, not a flake to be recorded: nothing here writes a flaky list. A
# diagnostic rerun of failed tests, if one is ever added, must write its output
# under the outdir and must never change the exit status.
#
#   ci/run.sh ci/suite.sh                  # from a sandbox without local Go
#   ci/suite.sh                            # on a runner with Go on PATH already
#   BOARD_CI_OUT=/path/to/artifacts ci/suite.sh   # keep the JUnit files
#   ci/suite.sh /path/to/artifacts               # same, as a positional argument
#   BOARD_CI_PLAYWRIGHT=skip ci/suite.sh         # everything but the Playwright step
#   BOARD_CI_PLAYWRIGHT=only ci/suite.sh         # only Playwright (and the web build, if none is stashed)
#
# The Go package matrix's JUnit (<outdir>/junit-go.xml) is also what ci/pkgtime
# reads in the Quality unit: a budgeted package (ci/pkgtime/budgets.json, or budgets-race.json under -race) whose
# <testsuite time> is over its budget fails the build.
#
# BOARD_CI_PLAYWRIGHT (operator ruling 001-WAVE, task 035): which parts of the
# run happen. Unset, or `with`, is the whole run, Playwright included (what a
# local Sweep runs). The Jenkinsfile runs two units from this
# one script, in parallel: Suite with `skip` (the web build and Vitest, the Go
# passes, the merged files; no Playwright, and no junit-playwright.xml)
# and E2E with `only` (the Playwright step, writing only the Playwright files,
# on the web build the Web UI unit stashed; no Vitest, no Go pass). Any other
# value is refused. The step itself is the same in `with` and
# `only`: every spec, once. The step also leaves <outdir>/junit-playwright-partial.xml
# (e2e/partial-junit-reporter.ts, tests finished so far) while it runs and removes it
# when the complete junit-playwright.xml exists: a run cut by its bound keeps it, and
# Quality's units section names the failing tests from it (task 006).
#
# The web UI is built once per CI build (R9 l): the Jenkinsfile's Web UI unit runs
# ci/web-build.sh (`npm ci`, `npm run build`, then a marker file in
# internal/webui/dist) and stashes dist; Suite, E2E and Quality unstash it. With
# that marker and an index.html present, this script runs no `npm run build`
# (Suite still runs `npm ci` in web/ for Vitest; E2E runs no web npm at all);
# with no marker (a local run, a dist built another way) it builds as before.
# ci/suitecheck pins both cases with a stub npm.
#
# BOARD_E2E_WORKERS (task 010, R6): how many Playwright workers the step runs,
# the one place the count reaches the config from. It is defined once, as
# E2E_WORKERS in the Jenkinsfile (2, at most 3 on int21h-ws), which exports it;
# this script refuses anything but a whole number from 1 to 3 (exit 2) and
# hands it to Playwright as E2E_WORKERS, which e2e/playwright.config.ts reads
# for `workers` (the config holds no worker number of its own). Unset, nothing
# is handed over and the config's local default of 1 applies. Why 2: the E2E
# unit alone on its node at one worker held a load of 1.3 to 2.7 (#291), so
# about 2 per worker, but its last stretch ran 7 to 11 at one worker (#288,
# #291), so a third worker would not stay under the load ceiling of 12.
# ci/suitecheck pins the single source and the limit of 3.
#
# Coverage: the unit pass runs with -coverpkg=./... (every package in the
# module, not just the ones a given test imports), and the per-pass
# covdata this script already collects is merged into one
# <outdir>/coverage-merged.out legacy profile (task 002, R187) in addition
# to the two separate per-pass profiles below. This script also unsets
# every BOARD_* variable from its own environment, right after reading the
# handful of BOARD_CI_* settings it honours itself, so a leaked
# BOARD_GODOG_PATHS/BOARD_HOME from the caller's shell cannot reach `go
# test` or the board binaries it spawns.
#
# How features/ is run
# ---------------------
# The gotestsum pass covers EVERY package, features/ included, with
# `-skip '^TestFeatures$'`; features/'s ordinary Go test functions (probes,
# harness self-tests, ...) run there like any other test. TestFeatures, the
# single Go test function that fans out into 300+ Godog scenarios via
# godog.TestSuite, is run alone, also through gotestsum, in a second pass: once
# per database leg (SQLite, then Postgres). A failing scenario fails its leg and
# so the script. When it fails, this script reads the pretty formatter's own
# "--- Failed steps:" summary (always emitted, see godogFormat() in
# features/godog_test.go) for each failed scenario's "<file>:<line>" and names
# them on stderr; a failure with no such summary is a whole-process abort (go
# test's -timeout alarm, a crash) and gets an explicit failed TestFeatures
# marker in the report input. Nothing is run a second time.
#
# Concurrent scenarios (task 023, R9 c): TestFeatures builds a harness for each
# scenario and runs the default Godog suite godogConcurrency scenarios at once
# (features/godog_test.go sets the number, ci/suitecheck pins it above 1). The
# scenarios tagged @dedicated-database (a Postgres container of their own) and
# @serve-upgrade (serve_upgrade.feature) run afterwards in a second suite of one
# scenario at a time. Godog's JUnit formatter writes its whole file per suite,
# so the second suite's file is junit-features-serial.xml beside
# junit-features.xml, and both are merged into junit-merged/junit-features.xml.
set -eu

GOTESTSUM_VERSION=${GOTESTSUM_VERSION:-v1.13.0}
gotestsum_pkg="gotest.tools/gotestsum@${GOTESTSUM_VERSION}"

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

# outdir (task 002, R187): an optional first positional argument, so a
# caller that already knows its own result directory does not have to go
# through the environment at all; BOARD_CI_OUT is still honoured when no
# argument is given, and the mktemp fallback below still applies when
# neither is set. `${1:-}` rather than `$1` because `set -u` would otherwise
# treat a plain no-argument invocation (every existing caller) as an
# unbound-variable error.
outdir=${1:-}
if [ -z "$outdir" ]; then
    outdir=${BOARD_CI_OUT:-}
fi
if [ -z "$outdir" ]; then
    outdir=$(mktemp -d "${TMPDIR:-/tmp}/board-ci-suite.XXXXXX")
fi
mkdir -p "$outdir"
# BOARD_E2E_WORKERS (see the header): read here, before the BOARD_* scrub.
e2e_workers=${BOARD_E2E_WORKERS:-}
case "$e2e_workers" in
    ""|1|2|3) ;;
    *)
        echo "ci/suite.sh: BOARD_E2E_WORKERS must be a whole number from 1 to 3 (got '$e2e_workers')" >&2
        exit 2
        ;;
esac
# BOARD_CI_PLAYWRIGHT (see the header): read here, before the BOARD_* scrub.
playwright_mode=${BOARD_CI_PLAYWRIGHT:-with}
case "$playwright_mode" in
    with|skip|only) ;;
    *)
        echo "ci/suite.sh: BOARD_CI_PLAYWRIGHT must be with, skip or only (got '$playwright_mode')" >&2
        exit 2
        ;;
esac
# Canonicalize to an absolute path now, once. features/TestFeatures's own
# test binary runs with its CWD set to the features/ package directory (an
# ordinary `go test` behaviour, not something this script controls), which
# is NOT $repo_root -- so a relative BOARD_CI_OUT (e.g. the CI workflow's
# "ci-results", chosen so the *host* steps outside the sibling container can
# use it relative to their own checkout) would make that binary's own
# BOARD_GODOG_JUNIT open fail with "no such file or directory" even though
# the exact same relative path is valid from repo_root. Every use of
# $outdir below (unit_coverprofile, go_junit, features_junit, covdir, ...)
# is now absolute, so it resolves the same regardless of which process's
# CWD interprets it.
outdir=$(cd "$outdir" && pwd)
echo "ci/suite.sh: writing JUnit/log files to $outdir"

overall_status=0

# --- 0. the web UI: build it into the binary's embed directory, then Vitest ---
# (task 007, R11). The build comes first so every Go pass below compiles and
# tests the binary with the real UI embedded rather than the placeholder page.
# Vitest (vitest run --coverage, configured in web/vitest.config.ts) writes
# its JUnit file to <outdir>/junit-vitest.xml and its @vitest/coverage-v8
# reports to <outdir>/web-coverage/. It runs once: a failure fails the suite.
# A checkout with no web/ (the suitecheck fixtures) skips this step.
vitest_junit="$outdir/junit-vitest.xml"
# The stashed web build (R9 l): ci/web-build.sh, run once per CI build by the
# Jenkinsfile's Web UI unit, leaves internal/webui/dist and a marker beside it,
# and the units that need the UI unstash both. With the marker (and an
# index.html) here, no web build runs in this script; without it (a local run, a
# dist built another way, a stale one) the script builds as it always did.
web_stashed=no
if [ -f internal/webui/dist/.ci-stashed-build ] && [ -f internal/webui/dist/index.html ]; then
    web_stashed=yes
fi
# The E2E unit (BOARD_CI_PLAYWRIGHT=only) builds the web UI for Playwright and
# writes none of Vitest's files.
if [ -f web/package.json ]; then
    vitest_log="$outdir/vitest.log"
    web_build_log="$outdir/web-build.log"
    if [ "$playwright_mode" = only ]; then
        web_build_log="$outdir/web-build-e2e.log"
    fi
    if [ "$web_stashed" = yes ]; then
        # The build is the Jenkins "Web UI" unit's, unstashed here (R9 l): no
        # `npm run build`. Vitest still needs web/node_modules, so Suite installs.
        echo "ci/suite.sh: using the stashed web UI build (internal/webui/dist), no web build"
        if [ "$playwright_mode" != only ] && ! (cd web && npm ci --no-audit --no-fund) > "$web_build_log" 2>&1; then
            echo "ci/suite.sh: web npm ci failed (see $web_build_log)" >&2
            overall_status=1
        fi
    elif ! (cd web && npm ci --no-audit --no-fund && npm run build) > "$web_build_log" 2>&1; then
        echo "ci/suite.sh: web build failed (see $web_build_log)" >&2
        overall_status=1
    fi
    if [ "$playwright_mode" != only ]; then
        if ! (cd web && WEB_JUNIT_FILE="$vitest_junit" WEB_COVERAGE_DIR="$outdir/web-coverage" npm run test) > "$vitest_log" 2>&1; then
            echo "ci/suite.sh: Vitest failed (see $vitest_log)" >&2
            overall_status=1
        fi
        if [ ! -s "$vitest_junit" ]; then
            echo "ci/suite.sh: Vitest wrote no JUnit file at $vitest_junit" >&2
            overall_status=1
        fi
    fi
fi

# --- 0b. Playwright: the real binary on a free port, the whole e2e suite (task 010, R11) ---
# e2e/fixtures.ts starts the binary built from this checkout (with the UI step 0
# just built embedded), one server per Playwright worker, each on a port, work
# directory, SQLite store and API key of its own (the config has no webServer,
# global setup or global teardown), and the config writes every reporter's output
# under $E2E_OUT. Playwright itself never
# retries (retries: 0) and neither does this script: the run happens once, and a
# failure fails the suite. The JUnit file lands at <outdir>/junit-playwright.xml
# and the Allure, trace and partial JUnit output in <outdir>/playwright/ (no HTML or JSON report). A
# checkout with no e2e/ (the suitecheck fixtures) skips this step.
#
# Overlap (task 018): Playwright (each worker has a server, free port and
# SQLite store of its own, e2e/fixtures.ts) shares nothing with the Go passes below, so it runs in the
# background beside them and is waited for before step 3, which reads its
# files. Its messages go to playwright-messages.log and are replayed to stderr
# at the wait, where a failure sets overall_status. Nothing is shortened: the
# same specs run with the same bounds.
# It exists because the sequential suite grew to the Suite stage's 45-minute
# budget, which stays as it is.
playwright_junit="$outdir/junit-playwright.xml"
# The tests finished so far, rewritten by e2e/partial-junit-reporter.ts as they
# end. A run cut by its bound (ci/unitrun.sh kills the script too) never reaches
# the copy below, and this file is then all of its JUnit results: Quality and the
# build report read it (ci/qualitycheck's units gate, docs/ci.md "Unit runs").
playwright_partial="$outdir/junit-playwright-partial.xml"
playwright_messages="$outdir/playwright-messages.log"
playwright_pid=""
# playwright_step is the whole step; it returns 1 when the suite must fail.
playwright_step() {
    playwright_failed=0
    playwright_log="$outdir/playwright.log"
    if ! (cd e2e && npm ci --no-audit --no-fund) > "$outdir/e2e-install.log" 2>&1; then
        echo "ci/suite.sh: e2e npm ci failed (see $outdir/e2e-install.log)" >&2
        playwright_failed=1
    fi
    rm -rf "$outdir/playwright"
    rm -f "$playwright_partial"
    rc=0
    (cd e2e && E2E_WORKERS="$e2e_workers" E2E_PARTIAL_JUNIT="$playwright_partial" E2E_OUT="$outdir/playwright" npx playwright test) > "$playwright_log" 2>&1 || rc=$?
    if [ -f "$outdir/playwright/junit.xml" ]; then
        cp "$outdir/playwright/junit.xml" "$playwright_junit"
        # The run ended, so the complete file stands in for the partial one.
        rm -f "$playwright_partial"
    fi
    if [ "$rc" -ne 0 ]; then
        echo "ci/suite.sh: Playwright failed (see $playwright_log)" >&2
        playwright_failed=1
    fi
    if [ ! -s "$playwright_junit" ]; then
        echo "ci/suite.sh: Playwright wrote no JUnit file at $playwright_junit" >&2
        playwright_failed=1
    fi
    return "$playwright_failed"
}
if [ "$playwright_mode" = only ]; then
    # The E2E unit: the step in the foreground, then its verdict. Nothing else
    # runs (no Vitest, no Go pass), so the unit's result files are Playwright's.
    if [ ! -f e2e/package.json ]; then
        echo "ci/suite.sh: BOARD_CI_PLAYWRIGHT=only but there is no e2e/package.json" >&2
        exit 1
    fi
    playwright_rc=0
    playwright_step || playwright_rc=$?
    if [ "$playwright_rc" -ne 0 ]; then
        overall_status=1
    fi
    exit "$overall_status"
fi
if [ "$playwright_mode" = with ] && [ -f e2e/package.json ]; then
    : > "$playwright_messages"
    playwright_step 2> "$playwright_messages" &
    playwright_pid=$!
fi

# --- 1. every Go test except TestFeatures, via gotestsum ---
# BOARD_CI_GO_PACKAGES lets a caller narrow the package list (used by this
# task's own local demonstration, so a scratch worktree's throwaway package
# can be exercised without also re-running the whole real suite); unset,
# the default is every package (./...), features/ included. The
# `-skip '^TestFeatures$'` below removes only the Godog entry point, which
# pass 2 owns; every other test function in features/ runs here.
pkgs=${BOARD_CI_GO_PACKAGES:-$(go list ./...)}

# BOARD_CI_GO_EXTRA_FLAGS (task 017, R145 nightly): extra `go test` flags
# appended, unquoted on purpose, to BOTH the unit pass below and the
# features/TestFeatures pass further down -- the nightly-only `-race` flag
# is the reason this exists, but it is a generic pass-through, not a
# race-specific hook. Unset (the default, every PR/push/dispatch run),
# this expands to nothing and both passes run exactly as before.
#
# `-covermode=atomic` is mandatory once `-race` is enabled (`go test`
# refuses `set`/`count` alongside it), so the unit pass's covermode tracks
# whether the extra flags mention -race rather than hardcoding `set`; every
# other caller (extra flags unset) keeps the original `set` mode.
#
# go_extra_flags captures the value NOW, before the BOARD_* scrub below
# unsets BOARD_CI_GO_EXTRA_FLAGS itself: both `go test` invocations expand
# this shell variable, never the (by then unset) environment variable, so
# the nightly lane's -race still reaches both passes (task 002 cure).
go_extra_flags=${BOARD_CI_GO_EXTRA_FLAGS:-}

# ci_sha / ci_run_url (task 005, R195): what the Allure results' own
# environment.properties and executor.json (step 3b) name. ci/run.sh forwards
# only BOARD_* variables into its container, so the workflow hands them in as
# BOARD_CI_SHA, BOARD_CI_RUN_URL (the Jenkins build link, BUILD_URL) and
# BOARD_CI_BUILD_NAME (e.g. "board/main #42"); all are captured here, before
# the BOARD_* scrub below, and all are optional -- a local run just records an
# unknown sha and no run link.
ci_sha=${BOARD_CI_SHA:-}
ci_run_url=${BOARD_CI_RUN_URL:-}
ci_build_name=${BOARD_CI_BUILD_NAME:-board CI}
# db_legs / ci_run_id / ci_docker_host (task 013, R4): the database legs the
# feature scenarios run on (SQLite always, first; every other named leg after
# it), and what the Postgres leg's harness needs to start its container through
# the docker socket ci/run.sh mounted: the run id the container is labelled with
# and the name the docker host answers to from inside this container. The
# BOARD_* scrub below would drop all three, and run_test_features hands them to
# the features pass explicitly.
db_legs=${BOARD_CI_DB_LEGS:-sqlite postgres}
ci_run_id=${BOARD_CI_RUN_ID:-}
ci_docker_host=${BOARD_CI_DOCKER_HOST:-}
covermode='set'
case " $go_extra_flags " in
    *' -race '*) covermode=atomic ;;
esac

# go_test_timeout / features_test_timeout (cure-01-07, R145 nightly
# completion): `go test`'s own default per-binary budget is 10 minutes
# (testing.(*M).startAlarm), applied independently to EACH package's test
# binary. That default is not one of this repo's own scenario deadlines --
# every individual scenario/step assertion in features/ carries its own
# much shorter context timeout (see features/assertions_test.go and
# friends) -- it is a harness-level aggregate budget nobody here ever set
# explicitly. features/TestFeatures fans out into 300+ Godog scenarios in
# one process, and as the suite has grown its wall time has crept up to
# within seconds of that unset 10m default: nightly run 36222292303's own
# suite step (the -race lane below) and its third ci/stability.sh
# repetition both hit `panic: test timed out after 10m0s` at 600.033s/
# 600.065s -- the harness's own execution budget, not any scenario's
# assertion, was too tight for the work it was asking that one process to
# finish (artifacts/review/nightly-timeout-stack.log,
# nightly-features-original.log). `-race` alone roughly doubles CPU cost,
# which is why the nightly-only lane hit it first, but the same panic also
# fired on a plain (non-race) ci/stability.sh run under ordinary CI
# contention -- so the fix widens the budget for every caller, not just
# the race lane. 25 minutes is a little over double the ~10m observed
# worst case, without touching a single scenario's own deadline.
features_test_timeout=${BOARD_CI_FEATURES_TEST_TIMEOUT:-25m}
unit_test_timeout=${BOARD_CI_UNIT_TEST_TIMEOUT:-15m}

# Scrub leaked BOARD_* environment (task 002, R187): every BOARD_CI_* setting
# this script itself reads has already been captured into a shell variable
# above (outdir, pkgs, go_extra_flags, covermode, features_test_timeout,
# unit_test_timeout) -- so any BOARD_* variable still present in the
# environment at this point came from the CALLER's own shell (a stray
# BOARD_GODOG_PATHS left over from a manual `BOARD_GODOG_PATHS=... go test
# ./features/` run, a BOARD_HOME pointed at a throwaway profile, ...) and
# must not be allowed to reach `go test`/the board binaries it spawns,
# which otherwise inherit it exactly like any other ordinary environment
# variable. run_test_features's own BOARD_GODOG_JUNIT/BOARD_GODOG_PATHS
# below are set per invocation through `env NAME=value ...`, which adds a
# variable to that one child's environment regardless of what this
# process has unset, so this scrub cannot remove the few BOARD_GODOG_*
# values the script sets on purpose.
for board_var in $(env | sed -n -e 's/^\(BOARD_[A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' -e 's/^\(VH_[A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' | sort -u); do
    unset "$board_var"
done

go_junit="$outdir/junit-go.xml"

# unit_coverprofile (R146/task 016 criterion 1, R146 cure-01-05): a
# legacy-format coverage profile covering every package this pass tests.
# The profile is not written by the *ordinary* `go test -coverprofile` flag:
# each `go test` invocation writes that path from scratch, so a second
# invocation into it would clobber the whole profile. Instead the unit pass
# uses the per-process-counter-file mechanism also used for features/'s
# black-box coverage below, reached through `-test.gocoverdir`, the internal
# test-binary flag `-coverprofile` itself sets under the hood (Go 1.20+; see
# `go help testflag`), not through the GOCOVERDIR *environment variable*: that
# env var only steers coverage for a separately built+run binary (go build
# -cover, as features/ does with the board binary itself); an ordinary `go
# test` invocation ignores it. Passed via `-args` (everything after -args goes
# to the test binary, not to `go test` itself), every `go test -cover`
# invocation writes its OWN uniquely-named counter file set into unit_covdir.
# `go tool covdata textfmt` below then bridges that whole directory into the
# one legacy-format unit_coverprofile file the rest of this script (and
# coverage_table) already expects.
unit_covdir="$outdir/covdata-unit"
# A directory left by an earlier run would be merged into this run's profile
# (counters keyed to source that has since changed), so start from empty.
rm -rf "$unit_covdir"
mkdir -p "$unit_covdir"
unit_coverprofile="$outdir/coverage-unit.out"

if [ -n "$pkgs" ]; then
    go_status=0
    # shellcheck disable=SC2086 # go_extra_flags is a space-separated flag list (BOARD_CI_GO_EXTRA_FLAGS); splitting it is intended
    go run "$gotestsum_pkg" \
        --format testname \
        --junitfile "$go_junit" \
        --packages "$pkgs" \
        -- -p=1 -count=1 -skip '^TestFeatures$' "-timeout=$unit_test_timeout" "-covermode=$covermode" -cover -coverpkg=./... $go_extra_flags -args "-test.gocoverdir=$unit_covdir" \
        || go_status=$?

    if [ "$go_status" -ne 0 ]; then
        echo "ci/suite.sh: Go package matrix failed (exit $go_status)" >&2
        overall_status=1
    fi
else
    echo "ci/suite.sh: no Go packages found (go list ./... returned nothing)" >&2
    overall_status=1
fi

# --- 2. features/TestFeatures: one run per database leg, no rerun ---
features_junit="$outdir/junit-features.xml"
features_junit_serial="$outdir/junit-features-serial.xml"
# allure_dir (task 005, R195/R196): the one Allure results directory of this
# run. The SQLite leg's features/ invocation below gets
# BOARD_GODOG_ALLURE=<this dir>, so features/'s own formatter writes native
# results into it. Step 3b adds the unit results, environment.properties and
# executor.json to the same directory.
allure_dir="$outdir/allure-results"
mkdir -p "$allure_dir"
features_log="$outdir/features.log"

# covdir (R146/task 016, criterion 2): a GOCOVERDIR passed to every
# features/TestFeatures invocation below. features/lifecycle_test.go's registerScenarioLifecycle
# Before hook reads this same env var from its own process's environment
# (inherited automatically by the board binaries it spawns, since
# StartScreenDriverInDir's cmd.Env starts from os.Environ()) and adds
# `go build -cover` only when it is non-empty, so every scenario's board
# process writes black-box coverage counters here. Each rebuilt binary and
# each spawned process gets its own uniquely-named counter file, so reusing
# one directory across the whole pass just accumulates more data rather than
# clobbering it.
covdir="$outdir/covdata-features"
rm -rf "$covdir"
mkdir -p "$covdir"

# count_counters / require_feature_counters (cure-01-04, R13): a features/
# leg whose log shows Godog ran scenarios must have left non-empty counter
# files in covdir, because the harness builds the black-box board binary
# with -cover whenever GOCOVERDIR is supplied. A leg that ran scenarios and
# left none means the binary was built (or run) without instrumentation, and
# the merged profile would silently be unit-only, so the suite fails instead.
feature_scenarios_ran=0
count_counters() {
    find "$covdir" -type f -name 'covcounters.*' -size +0c | wc -l | tr -d ' '
}

# require_feature_counters <leg> <log> <counters-before>: returns 1, saying
# why on stderr, when <log> shows scenarios ran but covdir gained no counters.
require_feature_counters() {
    rfc_leg=$1
    rfc_log=$2
    rfc_before=$3
    sed -E 's/\x1b\[[0-9;]*m//g' "$rfc_log" | grep -Eq '^[0-9]+ scenarios? \(' || return 0
    feature_scenarios_ran=1
    if [ "$(count_counters)" -le "$rfc_before" ]; then
        echo "ci/suite.sh: features/ on the $rfc_leg leg ran scenarios but wrote no coverage counters into $covdir: no usable feature coverage (is the board binary built with -cover when GOCOVERDIR is set?)" >&2
        return 1
    fi
    return 0
}

# run_test_features <gotestsum-junit-path> <NAME=value>...: one gotestsum-driven
# run of features/TestFeatures and nothing else. It is never rerun (see the
# header); --format standard-verbose keeps Godog's pretty output,
# including its "--- Failed steps:" summary, verbatim in the log. Every
# NAME=value goes through env(1) rather than a prefix assignment on the
# function call, whose export to child processes POSIX leaves unspecified.
# BOARD_FEATURES_COVERMODE carries the unit pass's covermode to the features
# harness: it builds the black-box board binary with `-cover`, and with -race
# on the unit pass (atomic counters) a `set`-mode binary leaves covdata that
# `go tool covdata textfmt` refuses to merge ("counter mode clash"), which
# emptied the nightly's merged profile and failed its quality gate.
run_test_features() {
    junit=$1
    shift
    # shellcheck disable=SC2086 # go_extra_flags is a space-separated flag list (BOARD_CI_GO_EXTRA_FLAGS); splitting it is intended
    env "BOARD_FEATURES_COVERMODE=$covermode" "BOARD_DB_LEG=$features_leg" "BOARD_CI_RUN_ID=$ci_run_id" "BOARD_CI_DOCKER_HOST=$ci_docker_host" "$@" go run "$gotestsum_pkg" \
        --format standard-verbose \
        --junitfile "$junit" \
        --packages ./features/ \
        -- -p=1 -count=1 -run '^TestFeatures$' "-timeout=$features_test_timeout" $go_extra_flags
}

# failed_scenario_locations <log>: the "<file>.feature:<line>" of every scenario
# Godog's own summary block names as failed in a run's log, one per line.
# Only that block ("--- Failed steps:" up to its "N scenarios (...)" tally)
# names failed scenarios; bounding the range keeps any later reprint of passing
# scenarios' headers (gotestsum's "=== Failed" recap) from being mistaken for a
# failure. The recap can repeat the summary block itself, hence sort -u.
failed_scenario_locations() {
    sed -E 's/\x1b\[[0-9;]*m//g' "$1" \
        | sed -n '/^--- Failed steps:/,/^[0-9][0-9]* scenarios (/p' \
        | grep -E '^[[:space:]]*Scenario' \
        | grep -oE '# [^[:space:]]+\.feature:[0-9]+' \
        | sed 's/^# //' \
        | sort -u
}

# The first leg is always SQLite; its files keep the names below, and every other
# leg of $db_legs runs after it (step 2b) into files of its own.
features_leg=sqlite
features_status=0
# features_aborted_without_locations (cure-01-08, R146): set only when the
# initial run fails AND the pretty log never printed a parseable
# "--- Failed steps:" summary at all -- the shape a whole-process abort
# (go test's own -timeout alarm firing mid-scenario, or any other crash
# that kills the binary before Godog reports anything) takes, as opposed
# to an ordinary scenario assertion failure, which Godog always reports
# before the process exits normally. This is exactly the case where
# BOARD_GODOG_JUNIT's own file is left empty/truncated (godog's JUnit
# formatter never got to flush), which is what step 3 below uses to decide
# whether a synthetic failed/aborted TestFeatures marker is needed so the
# report input never silently drops the pass.
features_aborted_without_locations=0
sqlite_counters_before=$(count_counters)
if ! run_test_features "$outdir/junit-features-gotestsum.xml" "BOARD_GODOG_JUNIT=$features_junit" "BOARD_GODOG_ALLURE=$allure_dir" "GOCOVERDIR=$covdir" > "$features_log" 2>&1; then
    features_status=1
    clean_log="$outdir/features.clean.log"
    sed -E 's/\x1b\[[0-9;]*m//g' "$features_log" > "$clean_log"

    locations=$(failed_scenario_locations "$features_log")

    if [ -z "$locations" ]; then
        echo "ci/suite.sh: features/ failed but no scenario location could be parsed from $features_log" >&2
        features_aborted_without_locations=1
    else
        for loc in $locations; do
            echo "ci/suite.sh: features/ scenario failed: $loc (see $features_log)" >&2
        done
    fi
fi

if [ "$features_status" -ne 0 ]; then
    echo "ci/suite.sh: features/ failed (see $features_log)" >&2
    overall_status=1
fi
if ! require_feature_counters sqlite "$features_log" "$sqlite_counters_before"; then
    overall_status=1
fi

# --- 2b. every other database leg: the same scenarios against Postgres (task 013, R4) ---
# One run of features/TestFeatures with BOARD_DB_LEG=<leg>; a failure fails the
# leg, and nothing is rerun. Everything lands in files named for the leg:
# features-<leg>.log and junit-features-<leg>-gotestsum.xml. Step 3c folds that
# into junit-merged/junit-features-<leg>.xml and compares the legs.
run_extra_leg() {
    leg=$1
    features_leg=$leg
    leg_log="$outdir/features-$leg.log"
    leg_counters_before=$(count_counters)
    if run_test_features "$outdir/junit-features-$leg-gotestsum.xml" "GOCOVERDIR=$covdir" > "$leg_log" 2>&1; then
        return 0
    fi
    leg_locations=$(failed_scenario_locations "$leg_log")
    if [ -z "$leg_locations" ]; then
        echo "ci/suite.sh: features/ on the $leg leg failed but no scenario location could be parsed from $leg_log" >&2
    else
        for loc in $leg_locations; do
            echo "ci/suite.sh: features/ scenario failed on the $leg leg: $loc (see $leg_log)" >&2
        done
    fi
    return 1
}

extra_legs=""
for leg in $db_legs; do
    [ "$leg" = sqlite ] && continue
    extra_legs="$extra_legs $leg"
    if ! run_extra_leg "$leg"; then
        echo "ci/suite.sh: features/ failed on the $leg leg" >&2
        overall_status=1
    fi
    if ! require_feature_counters "$leg" "$outdir/features-$leg.log" "$leg_counters_before"; then
        overall_status=1
    fi
done
features_leg=sqlite

# --- 2c. wait for the Playwright step started in 0b ---
if [ -n "$playwright_pid" ]; then
    playwright_rc=0
    wait "$playwright_pid" || playwright_rc=$?
    cat "$playwright_messages" >&2
    if [ "$playwright_rc" -ne 0 ]; then
        overall_status=1
    fi
fi

# --- 3. one merged JUnit file per pass (R146/task 019) ---
# ci/junitflaky (see its own header) reads each raw JUnit file and writes the
# merged one under junit-merged/, which ci/allure-report.sh and ci/summary.sh
# read. Every test runs once, so it never folds an attempt here. features/
# contributes Godog's own per-scenario JUnit; gotestsum's view of the same
# pass (TestFeatures/<scenario> subtests) is used instead when Godog's own
# file is missing, empty or malformed -- e.g. when the process was killed
# by go test's own -timeout alarm mid-scenario and Godog's JUnit formatter
# never got to flush (cure-01-08, R146: final-sha nightly 36222292303 left
# junit-features.xml empty this way, ci/junitflaky reported EOF on it, and
# this step used to just delete the merged features file outright,
# dropping every gotestsum scenario result the run DID produce and leaving
# the report showing only junit-go.xml -- an all-passing subset for a run
# that failed). merge_junit itself now reports success/failure to its
# caller (it used to always return 0, the bug that hid the EOF failure
# above from ever being noticed here) so this step can fall back instead of
# silently accepting an empty result.
merged_dir="$outdir/junit-merged"
mkdir -p "$merged_dir"

# merge_junit <output> <input>...: the inputs in chronological order; any
# that does not exist is left out. Returns 1 (output removed) if there was
# nothing to merge, or ci/junitflaky itself failed (e.g. malformed XML);
# returns 0 only once <output> has actually been written.
merge_junit() {
    merged_out=$1
    shift
    merge_inputs=""
    for f in "$@"; do
        [ -f "$f" ] && merge_inputs="$merge_inputs $f"
    done
    if [ -z "$merge_inputs" ]; then
        return 1
    fi
    # shellcheck disable=SC2086 # $outdir is canonical, one word per path
    if ! go run ./ci/junitflaky -o "$merged_out" $merge_inputs; then
        echo "ci/suite.sh: could not merge$merge_inputs into $merged_out" >&2
        rm -f "$merged_out"
        return 1
    fi
    return 0
}

# write_aborted_testfeatures_junit <path> <log> <gotestsum-merged 0|1>: a minimal, well-formed
# JUnit file carrying exactly one failed <testcase name="TestFeatures">,
# used (cure-01-08, R146) whenever the features/ pass aborted with no
# per-scenario evidence at all surviving from either Godog's own JUnit or
# gotestsum's view of the same run -- so the report input always states
# the failure explicitly rather than a downstream reader (ci/summary.sh,
# Allure) ever being able to mistake the absence of features/ data for an
# all-passing subset.
write_aborted_testfeatures_junit() {
    out_path=$1
    log_path=$2
    survived=$3
    if [ "$survived" -eq 1 ]; then
        detail="features/TestFeatures aborted before Godog could report any per-scenario result (see $log_path); Godog's own JUnit was unusable, so this pass's scenario results come from gotestsum's view of the same run"
    else
        detail="features/TestFeatures aborted before Godog could report any per-scenario result (see $log_path); no usable Godog or gotestsum JUnit survived this pass"
    fi
    detail=$(printf '%s' "$detail" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g')
    cat > "$out_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<testsuites tests="1" failures="1" errors="0" time="0">
	<testsuite name="features-aborted" tests="1" failures="1" errors="0" time="0">
		<testcase classname="features" name="TestFeatures">
			<failure message="$detail" type="aborted">$detail</failure>
		</testcase>
	</testsuite>
</testsuites>
EOF
}

merge_junit "$merged_dir/junit-go.xml" "$go_junit" || true
features_merged="$merged_dir/junit-features.xml"
features_merge_ok=0
if [ -s "$features_junit" ]; then
    if merge_junit "$features_merged" "$features_junit" "$features_junit_serial"; then
        features_merge_ok=1
    fi
fi
if [ "$features_merge_ok" -eq 0 ]; then
    if [ -s "$features_junit" ]; then
        echo "ci/suite.sh: godog JUnit for features/ ($features_junit) was malformed; falling back to gotestsum's own scenario results" >&2
    else
        echo "ci/suite.sh: godog JUnit for features/ ($features_junit) is missing or empty; falling back to gotestsum's own scenario results" >&2
    fi
    if merge_junit "$features_merged" "$outdir/junit-features-gotestsum.xml"; then
        features_merge_ok=1
    fi
fi

# cure-01-08 (R146): whenever the features/ pass itself failed, an explicit
# failed/aborted TestFeatures outcome must be part of the report input --
# either because nothing at all survived to merge above, or because this is
# specifically the whole-process-abort shape (features_aborted_without_
# locations, set in step 2) that leaves no scenario-level evidence of WHY it
# failed even when gotestsum's own view merged successfully.
if [ "$features_status" -ne 0 ] && { [ "$features_aborted_without_locations" -eq 1 ] || [ "$features_merge_ok" -eq 0 ]; }; then
    synthetic="$outdir/junit-features-aborted.xml"
    write_aborted_testfeatures_junit "$synthetic" "$features_log" "$features_merge_ok"
    if [ "$features_merge_ok" -eq 1 ]; then
        tmp_merged="$outdir/junit-features-with-abort.xml"
        if merge_junit "$tmp_merged" "$features_merged" "$synthetic"; then
            mv "$tmp_merged" "$features_merged"
        fi
    else
        if merge_junit "$features_merged" "$synthetic"; then
            features_merge_ok=1
        fi
    fi
fi

if [ "$features_merge_ok" -eq 0 ]; then
    echo "ci/suite.sh: no features/ JUnit data survived to merge (godog JUnit and gotestsum JUnit were both missing, empty or malformed)" >&2
fi

# --- 3c. per-leg JUnit, and the legs compared (task 013, R4) ---
# junit-merged/junit-features-<leg>.xml holds one leg's scenarios as gotestsum
# saw them (TestFeatures/<scenario>), read through ci/junitflaky like every
# merged file. They are separate files so that a scenario run on both legs is
# never read as a retry of itself. A scenario green on one leg and red on another
# fails the suite: ci/legcompare names it, and its report is leg-divergence.txt.
leg_args="sqlite=$merged_dir/junit-features-sqlite.xml"
merge_junit "$merged_dir/junit-features-sqlite.xml" "$outdir/junit-features-gotestsum.xml" || true
for leg in $extra_legs; do
    merge_junit "$merged_dir/junit-features-$leg.xml" "$outdir/junit-features-$leg-gotestsum.xml" || true
    leg_args="$leg_args $leg=$merged_dir/junit-features-$leg.xml"
done
if [ -n "$extra_legs" ]; then
    # shellcheck disable=SC2086 # one <leg>=<path> word per leg, paths hold no spaces
    if go run ./ci/legcompare $leg_args > "$outdir/leg-divergence.txt"; then
        echo "ci/suite.sh: every scenario gave the same answer on all database legs ($leg_args)"
    else
        cat "$outdir/leg-divergence.txt" >&2
        echo "ci/suite.sh: the database legs disagree, or could not be compared (see $outdir/leg-divergence.txt)" >&2
        overall_status=1
    fi
fi

# --- 3b. one Allure results directory: features native, unit converted, environment, executor (task 005, R195/R196) ---
# $allure_dir already holds features/'s native results (written by the
# formatter during step 2). The Go unit pass is converted from its merged
# JUnit by ci/junit2allure into the same directory under parentSuite "unit"
# (features/'s results carry parentSuite "features"), keeping the historyId
# Allure's own JUnit plugin used, so unit trends survive. features/'s JUnit is
# converted only where native results cannot speak for the pass: when none
# were written at all (the process died before the formatter flushed), or
# for the synthetic "aborted" marker, so a failed pass is never missing from
# the report. ci/allure-report.sh reads this directory; ci/summary.sh keeps
# reading the merged JUnit, which counts the same tests.
features_native=0
for f in "$allure_dir"/*-result.json; do
    [ -e "$f" ] && features_native=1
    break
done
if [ -s "$merged_dir/junit-go.xml" ]; then
    go run ./ci/junit2allure -group unit -o "$allure_dir" "$merged_dir/junit-go.xml" || {
        echo "ci/suite.sh: could not convert $merged_dir/junit-go.xml to Allure results" >&2
        overall_status=1
    }
fi
if [ "$features_native" -eq 0 ] && [ -s "$merged_dir/junit-features.xml" ]; then
    echo "ci/suite.sh: no native features/ Allure results; converting $merged_dir/junit-features.xml instead" >&2
    go run ./ci/junit2allure -group features -o "$allure_dir" "$merged_dir/junit-features.xml" || overall_status=1
elif [ -s "$outdir/junit-features-aborted.xml" ]; then
    go run ./ci/junit2allure -group features -o "$allure_dir" "$outdir/junit-features-aborted.xml" || overall_status=1
fi

# environment.properties: Allure's Environment widget. -race says whether the
# nightly flag reached both passes.
case " $go_extra_flags " in
    *' -race '*) race_mode=yes ;;
    *) race_mode=no ;;
esac
{
    printf 'go.version=%s\n' "$(go env GOVERSION 2>/dev/null || echo unknown)"
    printf 'git.sha=%s\n' "${ci_sha:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
    printf 'race=%s\n' "$race_mode"
} > "$allure_dir/environment.properties"

# executor.json: the Allure "Executor" widget, linking back to the Jenkins build.
if [ -n "$ci_run_url" ]; then
    printf '{"name":"Jenkins","type":"jenkins","buildName":"%s","buildUrl":"%s","reportName":"board CI"}\n' "$ci_build_name" "$ci_run_url" > "$allure_dir/executor.json"
else
    printf '{"name":"local run","type":"local","buildName":"local suite run","reportName":"board CI"}\n' > "$allure_dir/executor.json"
fi

# --- 4. coverage summary (R146/task 016, R146 cure-01-05) ---
# `unit_coverprofile` and `features_coverprofile` are both bridged the same
# way now: `unit_covdir`/`covdir` hold the GOCOVERDIR binary format, one
# counter file per `go test`/board process invocation, uniquely named rather
# than overwriting a shared one (see the comment above unit_covdir). `go tool
# covdata textfmt` bridges each directory into the same legacy text format
# `go tool cover` understands, so the one small awk aggregator below can
# read both without needing two unrelated summarizers.
#
# A covdir with no data (every process killed before its coverage atexit
# hook ran, or nothing ran at all) makes `textfmt` print a warning but
# still EXIT 0, writing a completely empty (zero-byte, no "mode:" header)
# file rather than failing -- so the fallback below cannot rely on the
# command's own exit status; it checks the resulting file is actually
# non-empty instead (cure-01-05: an empty section confuses the awk
# aggregator's FNR==1 file-boundary detection just as much as a missing
# one would, attributing the NEXT profile's own "mode:" header line to the
# wrong section and silently swapping its data into the other column --
# caught while proving this fix).
ensure_legacy_profile() {
    # ensure_legacy_profile <path>: guarantee <path> exists and has at
    # least the "mode: <mode>" header line every legacy coverage profile
    # needs, regardless of why it might otherwise be missing or empty.
    profile_path=$1
    if [ ! -s "$profile_path" ]; then
        printf 'mode: set\n' > "$profile_path"
    fi
}

features_coverprofile="$outdir/coverage-features.out"
if ! go tool covdata textfmt -i="$covdir" -o="$features_coverprofile" 2>"$outdir/covdata-textfmt.log"; then
    echo "ci/suite.sh: no features/ black-box coverage data in $covdir (see $outdir/covdata-textfmt.log)" >&2
fi
ensure_legacy_profile "$features_coverprofile"
if [ "$feature_scenarios_ran" -eq 1 ] && [ "$(($(wc -l < "$features_coverprofile") - 1))" -le 0 ]; then
    echo "ci/suite.sh: features/ ran scenarios but $features_coverprofile has no blocks: no usable feature coverage, so the merged profile would be unit-only" >&2
    overall_status=1
fi
if ! go tool covdata textfmt -i="$unit_covdir" -o="$unit_coverprofile" 2>"$outdir/covdata-unit-textfmt.log"; then
    echo "ci/suite.sh: no unit coverage data in $unit_covdir (see $outdir/covdata-unit-textfmt.log)" >&2
fi
ensure_legacy_profile "$unit_coverprofile"

# merged_coverprofile (task 002, R187): one profile covering BOTH passes'
# own covdata directories (unit_covdir, covdir), merged in a single
# `covdata textfmt -i=dir1,dir2` call rather than concatenating the two
# already-bridged legacy files above -- `covdata textfmt` itself does the
# per-package/per-block de-duplication a naive `cat` of two legacy
# profiles would not. The unit pass now also carries `-coverpkg=./...`
# (above), so unit_covdir already holds counters for every package in the
# module, not just the ones its own tests import directly, which is what
# makes this merged profile meaningfully wider than either input alone.
#
# `covdata textfmt` on an input set with no counter data at all (both
# directories empty, e.g. every tested package reporting "[no
# statements]") exits 0 and writes a zero-byte file, exactly like the
# per-pass case ensure_legacy_profile above already guards against -- so
# merged_blocks (every line after the `mode:` header ensure_legacy_profile
# guarantees) is what actually decides pass/fail here, not the command's
# own exit status.
merged_coverprofile="$outdir/coverage-merged.out"
if ! go tool covdata textfmt -i="$unit_covdir,$covdir" -o="$merged_coverprofile" 2>"$outdir/covdata-merged-textfmt.log"; then
    echo "ci/suite.sh: could not merge unit+features coverage into $merged_coverprofile (see $outdir/covdata-merged-textfmt.log)" >&2
fi
ensure_legacy_profile "$merged_coverprofile"
merged_blocks=$(($(wc -l < "$merged_coverprofile") - 1))
if [ "$merged_blocks" -le 0 ]; then
    echo "ci/suite.sh: merged coverage profile $merged_coverprofile has no blocks (unit+features coverage data was empty)" >&2
    overall_status=1
fi

# coverage_table <unit-profile> <features-profile>: prints one
# package-by-package table (plus a TOTAL row) with a column for each
# profile's statement coverage. Both profiles share the same legacy format
# (`mode: <mode>` then `<import/path/file.go>:<pos> <numstmt> <count>` lines),
# so a package name is just the line's file field with its own filename
# dropped. A package present in only one profile prints "-" in the other
# column rather than 0.0%, so "not exercised by this pass" stays visually
# distinct from "exercised, zero coverage".
coverage_table() {
    awk '
    function pkgof(f) {
        sub(/:.*/, "", f)
        n = split(f, parts, "/")
        pkg = parts[1]
        for (i = 2; i < n; i++) pkg = pkg "/" parts[i]
        return pkg
    }
    FNR == 1 { section++; next }
    {
        pkg = pkgof($1)
        numstmt = $2 + 0
        count = $3 + 0
        seen[pkg] = 1
        if (section == 1) {
            u_total[pkg] += numstmt
            if (count > 0) u_cov[pkg] += numstmt
            gu_total += numstmt
            if (count > 0) gu_cov += numstmt
        } else {
            f_total[pkg] += numstmt
            if (count > 0) f_cov[pkg] += numstmt
            gf_total += numstmt
            if (count > 0) gf_cov += numstmt
        }
    }
    END {
        printf "%-55s %10s %18s\n", "package", "unit", "features/ (black-box)"
        for (p in seen) {
            ustr = (p in u_total) ? sprintf("%.1f%%", (u_total[p] > 0 ? u_cov[p] * 100.0 / u_total[p] : 0)) : "-"
            fstr = (p in f_total) ? sprintf("%.1f%%", (f_total[p] > 0 ? f_cov[p] * 100.0 / f_total[p] : 0)) : "-"
            printf "%-55s %10s %18s\n", p, ustr, fstr
        }
        ut = (gu_total > 0) ? sprintf("%.1f%%", gu_cov * 100.0 / gu_total) : "0.0%"
        ft = (gf_total > 0) ? sprintf("%.1f%%", gf_cov * 100.0 / gf_total) : "0.0%"
        printf "%-55s %10s %18s\n", "TOTAL", ut, ft
    }' "$1" "$2"
}

coverage_summary="$outdir/coverage-summary.txt"
coverage_table "$unit_coverprofile" "$features_coverprofile" | tee "$coverage_summary"

echo "ci/suite.sh: done (outdir=$outdir, coverage_summary=$coverage_summary, coverage_merged=$merged_coverprofile)"
exit "$overall_status"
