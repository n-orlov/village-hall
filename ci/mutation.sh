#!/bin/sh
# ci/mutation.sh -- the Go mutation gate's entry point (SPEC 15.4, the
# Jenkins Mutation stage's command). Runs gremlins over every package listed
# in ci/quality.json's mutation_go.packages (the Phase 0/1 domain and store
# plus the Phase 2 application logic; internal/server, internal/cli and
# internal/tasks are narrowed to the files in mutation_go.scopes, and
# `ci/mutationgate -unmeasured` lists any application file changed since
# mutation_go.phase_base that no unit measures), writes one JSON report per
# package, then runs
# ci/mutationgate, which fails below mutation_go.efficacy or when a package
# generated no mutant. It then runs Stryker over web/src/lib and fails below
# stryker.break (whole run) or stryker.module_break (any one module). The
# thresholds live in ci/quality.json only.
#
# The tests gremlins runs (its covered run and every mutant's) run on the
# SQLite leg only: this script sets BOARD_DB_LEG=sqlite for them, so no mutant
# of internal/server, internal/store or internal/feed starts a Postgres
# container (their Postgres-only tests run as a postgres subtest only on the
# Postgres leg, and their container helpers fail on BOARD_DB_LEG=sqlite). The Postgres leg of those packages and of features/TestFeatures
# runs in the Suite unit (ci/suite.sh), which still runs both legs.
#
#   ci/run.sh ci/mutation.sh                  # reports in ci-results/mutation
#   ci/run.sh ci/mutation.sh /tmp/m [config]  # a test's own outdir and config
#   ci/run.sh ci/mutation.sh ci-results/mutation ci/quality.json logic
#                                             # one lane of mutation_go.lanes
#   ci/run.sh ci/mutation.sh ci-results/m1 ci/quality.json server-core,logic
#                                             # several lanes in one run
#
# A lane is a group of packages that can be run on their own. In full mode the
# Jenkinsfile runs three units in parallel, each one ci/mutation.sh over a
# comma-separated list of lanes in its own outdir, and ci/mutation-merge.sh
# judges the union of their reports; in diff mode one unit runs the lane "all"
# restricted to the changed lines. The lane "all" (the default) is every
# package plus Stryker. Stryker runs when the lane list holds "base" or "all",
# and only then.
# A package split into parts (mutation_go.parts) is run once per part, a part
# written "internal/server#ops", with its own report and its own judgement.
# A package's report is written as soon as that package finishes (to a .part
# file, renamed whole), so a unit cut by its bound (ci/unitrun.sh) keeps the
# reports of the packages it finished, the Jenkinsfile stashes and archives
# them, and the merge gate names only the unfinished packages as missing
# ("the package never ran"), each finished one judged on its efficacy.
# Each lane judges and replaces only its own packages' reports, so the lanes
# leave the reports of all of them in the one directory ci/quality.sh reads.
#
# Diff mode (BOARD_MUTATION_DIFF=<ref>, the Jenkinsfile's branch and PR
# builds): only the units with a measured file changed since the merge base
# with <ref> run, gremlins mutates only the changed lines (gremlins --diff,
# which compares the working tree with `git diff --merge-base <ref>`), and
# ci/mutationgate -diff judges them: a unit with no mutant passes, one with
# mutants is held to the same efficacy floor. Stryker runs only when
# web/src/lib changed since that merge base (then over all of it). The
# unchanged units are never handed to gremlins: with an empty diff gremlins
# mutates everything.
#
# BOARD_MUTATION_WORKERS: gremlins' worker count (default: one per CPU).
# BOARD_MUTATION_TEST_CPU: GOMAXPROCS for gremlins and every go build and test
# it runs, so workers x test CPUs stays near the core count. Not gremlins'
# own --test-cpu: v0.6.0 passes it as the single argument "-cpu N", which go
# test rejects, so every mutant would count as KILLED.
# BOARD_MUTATION_STRYKER_CONCURRENCY: Stryker's test runner processes
# (default: Stryker's own, one per CPU less one). The Jenkinsfile caps it so
# that Stryker, which runs at the end of one full-mode unit while the others
# still run gremlins, does not take every core beside them.
set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

outdir=${1:-ci-results/mutation}
config=${2:-ci/quality.json}
lane=${3:-all}

# Read before the scrub below (see the header).
workers=${BOARD_MUTATION_WORKERS:-}
test_cpu=${BOARD_MUTATION_TEST_CPU:-}
stryker_concurrency=${BOARD_MUTATION_STRYKER_CONCURRENCY:-}
diff_ref=${BOARD_MUTATION_DIFF:-}
# The attempt's run id (ci/run.sh forwards RALPHD_RUN_ID as BOARD_CI_RUN_ID): the
# tests gremlins runs label their Postgres testcontainers with it, so the
# Jenkinsfile's per-attempt cleanup reaps the ones a killed mutant run leaves.
ci_run_id=${BOARD_CI_RUN_ID:-}

# Same scrub as ci/suite.sh and ci/quality.sh: a stray BOARD_* variable must
# never reach the tests gremlins runs.
# The exceptions, handed to gremlins explicitly below: BOARD_CI_RUN_ID, and
# BOARD_DB_LEG=sqlite (the Postgres leg is not run here, see the header).
for board_var in $(env | sed -n -e 's/^\(BOARD_[A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' -e 's/^\(VH_[A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' | sort -u); do
    unset "$board_var"
done

# gremlins bounds each mutant's test run by a multiple (--timeout-coefficient,
# an integer) of its covered run's time, and a mutant over the bound is TIMED
# OUT: judged, but neither killed nor lived. With a warm build cache a small
# package's covered run takes a fraction of a second, less than the rebuild a
# mutant needs, so at gremlins' default of 3 every mutant of a package like
# internal/store timed out and none was judged. 60 is the multiple that judges
# every mutant of a small package; a mutant that really loops still ends.
#
# The same 60 made a hanging mutant of internal/server (covered run about
# 65 s) wait 65 minutes: #233's server#core part spent over two hours of its
# 4 h 41 min on seven idempotency.go mutants that never return, every one
# TIMED OUT in the end. So the multiple is capped by a bound on the mutant's
# time, mutant_bound_s (300 s): before a package directory's first gremlins run
# the script times that directory's covered suite once, `go test -cover`
# under the same GOMAXPROCS and nice as gremlins, and passes
# max(min_coefficient, min(60, mutant_bound_s / that time)). The rule is the
# same for every package: a covered run under 5 s keeps 60, internal/feed
# (22 s) gets 13, internal/cli (55 s) and internal/server (65 s) get 5, the
# floor. A mutant that lives runs its package's suite once, about 1.5 times
# the covered run in #233 (the build of the mutant on top), so the floor of 5
# keeps every such mutant LIVED instead of TIMED OUT even with the three
# Jenkins units sharing the host; a hanging one is still TIMED OUT, about
# 5 minutes after it started instead of an hour. No verdict changes kind.
timeout_coefficient_max=60
min_coefficient=5
mutant_bound_s=300

# coefficient_for <dir>: the --timeout-coefficient for a package directory,
# timed once per directory and run (a package split into parts shares it).
coefficient_cache=$(mktemp)
coefficient_for() {
    cached=$(sed -n "s|^$1 ||p" "$coefficient_cache")
    if [ -n "$cached" ]; then
        echo "$cached"
        return
    fi
    start=$(date +%s)
    # Its verdict is gremlins' to give: a failing suite fails gremlins' own
    # covered run, so only the time counts here.
    env ${ci_run_id:+BOARD_CI_RUN_ID=$ci_run_id} BOARD_DB_LEG=sqlite ${test_cpu:+GOMAXPROCS=$test_cpu} \
        nice -n 10 go test -cover "./$1/..." >/dev/null 2>&1 || true
    took=$(( $(date +%s) - start ))
    [ "$took" -ge 1 ] || took=1
    coef=$(( mutant_bound_s / took ))
    [ "$coef" -le "$timeout_coefficient_max" ] || coef=$timeout_coefficient_max
    [ "$coef" -ge "$min_coefficient" ] || coef=$min_coefficient
    echo "$1 $coef" >>"$coefficient_cache"
    echo "ci/mutation.sh: covered run of ./$1 took ${took}s: --timeout-coefficient $coef" >&2
    echo "$coef"
}

# gremlins takes that covered run's time from `go test -cover`, and go's test
# cache can answer that run in a few milliseconds whatever the suite really
# costs: a package whose tests take 30 s then gets a bound of seconds and every
# mutant of it times out unjudged. -count=1 makes the covered run a real run,
# so the coefficient multiplies the suite's real duration. The coefficient
# itself is unchanged. -timeout=30m: go test's default 10 min kills the covered
# run of internal/server on an arm64 phone (about 5 min on amd64, over 10 there).
GOFLAGS="${GOFLAGS:+$GOFLAGS }-count=1 -timeout=30m"
export GOFLAGS

trap 'rm -f "$coefficient_cache"' EXIT

mkdir -p "$outdir"
rm -f "$outdir/unit-verdict.txt" "$outdir/mutationgate.txt"

# Every report of the lane goes, so a diff-mode run never leaves an older
# full run's report of an unchanged unit behind.
for pkg in $(go run ./ci/mutationgate -config "$config" -list -lane "$lane"); do
    rm -f "${outdir:?}/$(printf '%s' "$pkg" | tr '/#' '--').json" "${outdir:?}/$(printf '%s' "$pkg" | tr '/#' '--').json.part"
done
if [ -n "$diff_ref" ]; then
    echo "ci/mutation.sh: diff mode against the merge base with $diff_ref ($(git merge-base "$diff_ref" HEAD))"
    # gremlins v0.6.0 --diff runs `git diff --merge-base <ref>` in the module
    # root, which prints paths from the root, and matches them against paths
    # relative to the package it mutates ("list.go", not
    # "internal/notes/list.go"): nothing ever matches, every mutant is
    # SKIPPED. A git shim first in gremlins' PATH adds --relative=<package
    # dir> to that one command, so the paths are the package's own. Every
    # other git command passes through untouched.
    real_git=$(command -v git)
    shim=$(mktemp -d)
    trap 'rm -rf "$shim" "$coefficient_cache"' EXIT
    cat >"$shim/git" <<EOF
#!/bin/sh
if [ "\$1" = diff ] && [ -n "\${MUTATION_DIFF_RELATIVE:-}" ]; then
    shift
    exec "$real_git" diff --relative="\$MUTATION_DIFF_RELATIVE" "\$@"
fi
exec "$real_git" "\$@"
EOF
    chmod +x "$shim/git"
fi
packages=$(go run ./ci/mutationgate -config "$config" -list -lane "$lane" ${diff_ref:+-diff "$diff_ref"})
if [ -n "$diff_ref" ] && [ -z "$packages" ]; then
    echo "ci/mutation.sh: no measured Go file of lane $lane changed: no gremlins run"
fi
for pkg in $packages; do
    name=$(printf '%s' "$pkg" | tr '/#' '--')
    dir=${pkg%%#*}
    # A package with a scope in quality.json is narrowed to its listed files:
    # every other file of it is excluded (one -E per file, from mutationgate).
    set --
    for pattern in $(go run ./ci/mutationgate -config "$config" -excludes "$pkg"); do
        set -- "$@" -E "$pattern"
    done
    timeout_coefficient=$(coefficient_for "$dir")
    echo "ci/mutation.sh: gremlins unleash ./$pkg ($# exclude flags, --timeout-coefficient $timeout_coefficient)"
    # nice: a full run takes every core for hours; other jobs on the host win.
    # The GOMAXPROCS cap applies only here, never to the gate's own go run.
    env ${ci_run_id:+BOARD_CI_RUN_ID=$ci_run_id} BOARD_DB_LEG=sqlite ${test_cpu:+GOMAXPROCS=$test_cpu} ${diff_ref:+PATH="$shim:$PATH" MUTATION_DIFF_RELATIVE="$dir"} \
        nice -n 10 gremlins unleash "./$dir" \
        --timeout-coefficient "$timeout_coefficient" ${workers:+--workers "$workers"} \
        ${diff_ref:+--diff "$diff_ref"} "$@" -o "$outdir/$name.json.part"
    # The package's report appears, whole, the moment the package finishes: a
    # unit cut by its bound later keeps every finished package's report, and
    # the cut one leaves no half-written file (only a .part, which no reader
    # takes for a report).
    # (gremlins that wrote no report leaves none: the gate names it missing.)
    if [ -f "$outdir/$name.json.part" ]; then
        mv "$outdir/$name.json.part" "$outdir/$name.json"
    fi
done

status=0
# The unit's own verdicts go to "$outdir/unit-verdict.txt" (one "<gate>:
# passed|failed|not run" line per gate, the names of ci/quality.json's gates),
# which the build's CI summary page (ci/jenkins-report.sh) shows for the
# mutation-go and stryker rows that a Core quality run defers. It only records
# what this script already judged: the exit status is unchanged.
go_verdict=passed
go run ./ci/mutationgate -config "$config" -dir "$outdir" -lane "$lane" ${diff_ref:+-diff "$diff_ref"} \
    > "$outdir/mutationgate.txt" 2>&1 || go_verdict=failed
cat "$outdir/mutationgate.txt"
[ "$go_verdict" = passed ] || status=1

# The frontend: Stryker over web/src/lib (web/stryker.config.mjs), then the
# per-module break from ci/quality.json (web/scripts/mutation/mutation.ts).
# Its JSON report is copied to "$outdir/stryker/mutation.json", where
# ci/quality.sh's stryker row reads it.
stryker=no
stryker_verdict="not run"
case ",$lane," in
*,all,*|*,base,*) stryker_lane=yes ;;
*) stryker_lane=no ;;
esac
if [ -f web/stryker.config.mjs ] && [ "$stryker_lane" = yes ]; then
    stryker=yes
    if [ -n "$diff_ref" ] && git diff --quiet --merge-base "$diff_ref" -- web/src/lib; then
        echo "ci/mutation.sh: web/src/lib unchanged since the merge base with $diff_ref: no stryker run"
        stryker=no
    fi
fi
if [ "$stryker" = yes ]; then
    echo "ci/mutation.sh: stryker over web/src/lib"
    stryker_verdict=passed
    mkdir -p "$outdir/stryker"
    rm -f "$outdir/stryker/mutation.json"
    if [ ! -x web/node_modules/.bin/stryker ]; then
        (cd web && npm ci --no-audit --no-fund) || { status=1; stryker_verdict=failed; }
    fi
    # STRYKER_CONCURRENCY: web/stryker.config.mjs reads it; nice as gremlins'.
    (cd web && rm -f reports/mutation/mutation.json &&
        env ${stryker_concurrency:+STRYKER_CONCURRENCY=$stryker_concurrency} nice -n 10 npm run mutation) ||
        { status=1; stryker_verdict=failed; }
    [ -f web/reports/mutation/mutation.json ] && cp web/reports/mutation/mutation.json "$outdir/stryker/mutation.json"
fi
printf 'mutation-go: %s\nstryker: %s\n' "$go_verdict" "$stryker_verdict" > "$outdir/unit-verdict.txt"
exit "$status"
