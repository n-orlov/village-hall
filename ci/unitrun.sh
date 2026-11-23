#!/bin/sh
# ci/unitrun.sh — run one CI unit's command under a time bound, keep whatever
# it wrote when it fails or is cut, and say which tests failed.
#
#   ci/unitrun.sh <unit name> <bound seconds> <results dir> -- <command> [args...]
#
# The Jenkinsfile runs the Suite and E2E units and the three full mutation
# units through it (unitSh in the Jenkinsfile; docs/ci.md "Unit runs").
#
# - The command runs under timeout(1) (its own process group, TERM at the
#   bound, KILL 15 s later). A command that outlives the bound is cut: the
#   script exits 124, and removes the sibling containers of this attempt
#   (label ralphd.run=$RALPHD_RUN_ID with ralphd.role=sibling, by exact ID,
#   never the job's own container) so nothing keeps writing while the
#   results are stashed. Any other exit status is passed through.
# - Nothing is deleted. The results directory (ci-results in the pipeline)
#   keeps every junit-*.xml, log, flaky-*.txt and finished report the command
#   wrote before it failed or was cut, and the Jenkinsfile's stash and archive
#   steps take them from there. unit-kept-<slug>.txt lists them.
# - On a failure or a cut, unit-failure-<slug>.txt in the results directory,
#   and the console, get a failure summary: how the unit ended, then every
#   failing test read from the results directory's junit-*.xml (the test's
#   classname and name: spec file and title for Playwright and Vitest,
#   package and test for Go, feature file and scenario for Godog), one line
#   each, grouped by JUnit file. A pass writes no summary.
#
# <bound seconds> is the part of the unit's own limit left for its command;
# the Jenkinsfile passes the attempt's limit less the time already spent and
# a grace for this script's own summary. The bound is only ever lower than
# the unit's limit, never higher.
set -u

usage() {
    echo "usage: ci/unitrun.sh <unit name> <bound seconds> <results dir> -- <command> [args...]" >&2
    exit 2
}

[ $# -ge 5 ] || usage
name=$1
bound=$2
outdir=$3
shift 3
[ "$1" = "--" ] || usage
shift
case $bound in
    '' | *[!0-9]*) usage ;;
esac
[ "$bound" -gt 0 ] || usage

slug=$(printf '%s' "$name" | tr 'A-Z' 'a-z' | sed 's/[^a-z0-9][^a-z0-9]*/-/g; s/^-//; s/-$//')
summary="$outdir/unit-failure-$slug.txt"
kept="$outdir/unit-kept-$slug.txt"
mkdir -p "$outdir"
rm -f "$summary" "$kept"

started=$(date +%s)
timeout -k 15 "$bound" "$@"
rc=$?
elapsed=$(($(date +%s) - started))

if [ "$rc" -eq 0 ]; then
    exit 0
fi

cut=no
if { [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; } && [ "$elapsed" -ge "$bound" ]; then
    cut=yes
    rc=124
    # Sibling containers of this attempt only: the id names the job, the
    # build and the unit, and the role keeps the job's own container out.
    if [ -n "${RALPHD_RUN_ID:-}" ] && command -v docker >/dev/null 2>&1; then
        ids=$(docker ps -aq --filter "label=ralphd.run=$RALPHD_RUN_ID" --filter label=ralphd.role=sibling 2>/dev/null)
        [ -z "$ids" ] || docker rm -f $ids >/dev/null 2>&1
    fi
fi

# Every failing test of one JUnit file as "classname :: name", one per line.
# A <testcase> that holds a <failure> or an <error> fails; one with only
# <skipped>, or a self-closing one, does not. The file is read as one line,
# split at each <testcase>, so attribute order and line breaks do not matter.
failing_tests() {
    tr '\n\r' '  ' <"$1" |
        sed 's/<testcase[ >]/\
&/g; s/<\/testsuite>/\
/g' |
        awk '
            function attr(tag, key,   m, s) {
                s = tag
                if (match(s, "[ ]" key "=\"[^\"]*\"") == 0) return ""
                m = substr(s, RSTART + length(key) + 3, RLENGTH - length(key) - 4)
                gsub(/&quot;/, "\"", m); gsub(/&apos;/, "\047", m)
                gsub(/&lt;/, "<", m); gsub(/&gt;/, ">", m); gsub(/&amp;/, "\\&", m)
                return m
            }
            /^<testcase[ >]/ {
                if ($0 !~ /<failure[ >\/]/ && $0 !~ /<error[ >\/]/) next
                tag = $0
                sub(/>.*/, "", tag)
                cn = attr(tag, "classname"); tn = attr(tag, "name")
                if (cn != "" && tn != "") print cn " :: " tn
                else if (tn != "") print tn
                else if (cn != "") print cn
                else print "(unnamed test case)"
            }'
}

{
    if [ "$cut" = yes ]; then
        echo "unit $name: cut at $((bound / 60)) min $((bound % 60)) s (its bound); the command was killed, results so far kept"
    else
        echo "unit $name: failed with exit status $rc after $((elapsed / 60)) min $((elapsed % 60)) s"
    fi
    total=0
    list=$(mktemp)
    for f in "$outdir"/junit-*.xml; do
        [ -f "$f" ] || continue
        failing_tests "$f" | sort -u >"$list.one"
        n=$(wc -l <"$list.one" | tr -d ' ')
        [ "$n" -gt 0 ] || continue
        total=$((total + n))
        base=$(basename "$f")
        sed "s|^|  $base: |" "$list.one" >>"$list"
    done
    if [ "$total" -eq 0 ]; then
        junits=$(find "$outdir" -maxdepth 1 -name 'junit-*.xml' | wc -l | tr -d ' ')
        echo "failing tests: none named (junit-*.xml files read: $junits)"
    else
        echo "failing tests: $total"
        head -n 200 "$list"
        [ "$total" -le 200 ] || echo "  ... and $((total - 200)) more in the junit files"
    fi
    rm -f "$list" "$list.one"
} >"$summary"

find "$outdir" -type f \( -name 'junit-*.xml' -o -name '*.log' -o -name 'flaky-*.txt' -o -name '*.json' \) 2>/dev/null | sort >"$kept"

cat "$summary"
exit "$rc"
