#!/bin/sh
# ci/loadsample.sh — sample the machine's load average, CPU busy and iowait
# while a build runs (R6: the number of Playwright workers is chosen from, and
# the load ceiling of 12 is judged on, these samples).
#
#   ci/loadsample.sh [-i seconds] [-w seconds] [-p procdir] <file>
#   ci/loadsample.sh [-i seconds] [-w seconds] [-p procdir] <file> -- <command> [args...]
#
# One row is appended to <file> per interval (-i, default 60), as one line of
# space-separated fields:
#
#   <UTC timestamp> load1=<1-minute load average> busy=<CPU busy %> iowait=<CPU iowait %>
#
# load1 is the first field of <procdir>/loadavg. busy and iowait are shares of
# all CPU time spent between two reads of the first ("cpu") line of
# <procdir>/stat: iowait / total, and (total - idle - iowait) / total, where
# total is user + nice + system + idle + iowait + irq + softirq + steal. A
# machine that did no accounted work between two reads gives 0.0 for both.
# <procdir> defaults to /proc (-p exists for the test that feeds fake files).
#
# The first row is written -w seconds (default 1) after the start, so that it
# has a delta to measure, and the following ones every interval. A final row is
# written when the sampler stops.
#
# Without a command it samples until it receives TERM or INT, writes its final
# row and exits 0. With `-- <command>` it samples for exactly the command's run
# (the first row is on disk before the command starts) and exits with the
# command's exit status: the Jenkinsfile wraps the E2E unit's command in it, on
# int21h-ws, and the file lands in ci-results, which the report archives.
#
# Plain POSIX sh: it runs on the agent itself, outside the CI container.
set -u

interval=60
warm=1
proc=/proc
while getopts 'i:w:p:' opt; do
    case $opt in
        i) interval=$OPTARG ;;
        w) warm=$OPTARG ;;
        p) proc=$OPTARG ;;
        *) exit 2 ;;
    esac
done
shift $((OPTIND - 1))
usage() {
    echo "usage: ci/loadsample.sh [-i seconds] [-w seconds] [-p procdir] <file> [-- <command> [args...]]" >&2
    exit 2
}
[ $# -ge 1 ] || usage
out=$1
shift
if [ $# -gt 0 ]; then
    [ "$1" = "--" ] && [ $# -ge 2 ] || usage
    shift
    "$0" -i "$interval" -w "$warm" -p "$proc" "$out" &
    sampler=$!
    # The first row is on disk, and the sampler's signal handler installed,
    # before the command starts (a TERM that arrived earlier would leave the
    # sampler's sleep behind).
    waited=0
    while [ ! -s "$out" ] && [ "$waited" -lt 100 ]; do
        sleep 0.1
        waited=$((waited + 1))
    done
    "$@"
    rc=$?
    kill -TERM "$sampler" 2>/dev/null
    wait "$sampler" 2>/dev/null
    exit "$rc"
fi

mkdir -p "$(dirname "$out")"

# cpu_counters sets total, idle and iowait from the "cpu" line of procdir/stat.
cpu_counters() {
    read -r _ user nice system idle iowait irq softirq steal _ <"$proc/stat" || return 1
    total=$((user + nice + system + idle + iowait + irq + softirq + steal))
    idle_t=$idle
    iowait_t=$iowait
}

# tenths prints $1 * 100 / $2 with one decimal (0.0 when $2 is 0).
tenths() {
    if [ "$2" -le 0 ]; then
        printf '0.0'
        return
    fi
    t=$((1000 * $1 / $2))
    printf '%d.%d' $((t / 10)) $((t % 10))
}

cpu_counters || { echo "ci/loadsample.sh: cannot read $proc/stat" >&2; exit 1; }
prev_total=$total prev_idle=$idle_t prev_iowait=$iowait_t

row() {
    cpu_counters || return 0
    read -r load1 _ <"$proc/loadavg" || load1=0
    dt=$((total - prev_total))
    di=$((idle_t - prev_idle))
    dw=$((iowait_t - prev_iowait))
    prev_total=$total prev_idle=$idle_t prev_iowait=$iowait_t
    printf '%s load1=%s busy=%s iowait=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$load1" \
        "$(tenths $((dt - di - dw)) "$dt")" "$(tenths "$dw" "$dt")" >>"$out"
}

nap=
stop() {
    [ -z "$nap" ] || kill "$nap" 2>/dev/null
    row
    exit 0
}
trap stop TERM INT

# sleep runs in the background and is waited on, so a signal ends the wait at
# once instead of after the interval.
pause() {
    sleep "$1" >/dev/null 2>&1 &
    nap=$!
    wait "$nap"
    nap=
}

pause "$warm"
while :; do
    row
    pause "$interval"
done
