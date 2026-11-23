#!/bin/sh
# ci/loadsample.sh — sample the machine's load average, CPU busy and iowait
# while a build runs (R6: the number of Playwright workers is chosen from, and
# the load ceiling of 12 is judged on, these samples).
#
#   ci/loadsample.sh [-i seconds] [-w seconds] [-n snapshots] [-p procdir] <file>
#   ci/loadsample.sh [-i seconds] [-w seconds] [-n snapshots] [-p procdir] <file> -- <command> [args...]
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
# Who the load is (R6, task 020): load1 says how much, not whose. After each
# interval row the sampler also takes -n snapshots (default 5 when the interval
# is a whole number of at least 10 seconds, else none), one second apart, of
# every thread that is runnable or in uninterruptible sleep (the states the load
# average counts, read from <procdir>/<pid>/task/<tid>/stat), and appends one line to
# <file without .txt>-procs.txt:
#
#   <UTC timestamp> runnable=<mean per snapshot> comm <name>=<mean> ... cgroup <group>=<mean> ...
#
# comm lists the ten largest process names (a node process is named
# node:<its script>, e.g. node:vitest.mjs), cgroup the five largest control
# groups (the last component of the process's cgroup path, so a CI container, the
# agent's service and anything else on the machine tell apart). The next
# interval's wait is shortened by the snapshot time, so rows stay one interval
# apart. The sampler's own processes count in runnable (one or two).
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
nsnap=
while getopts 'i:w:n:p:' opt; do
    case $opt in
        i) interval=$OPTARG ;;
        w) warm=$OPTARG ;;
        n) nsnap=$OPTARG ;;
        p) proc=$OPTARG ;;
        *) exit 2 ;;
    esac
done
shift $((OPTIND - 1))
usage() {
    echo "usage: ci/loadsample.sh [-i seconds] [-w seconds] [-n snapshots] [-p procdir] <file> [-- <command> [args...]]" >&2
    exit 2
}
[ $# -ge 1 ] || usage
out=$1
shift
if [ $# -gt 0 ]; then
    if [ "$1" != "--" ] || [ $# -lt 2 ]; then
        usage
    fi
    shift
    "$0" -i "$interval" -w "$warm" -n "$nsnap" -p "$proc" "$out" &
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

# Snapshots default: five when the interval is a whole number of 10 s or more.
if [ -z "$nsnap" ]; then
    nsnap=0
    case $interval in
        *[!0-9]* | '') ;;
        *) [ "$interval" -ge 10 ] && nsnap=5 ;;
    esac
fi
case $nsnap in *[!0-9]* | '') echo "ci/loadsample.sh: -n wants a whole number" >&2; exit 2 ;; esac
case $out in
    *.txt) procs_out=${out%.txt}-procs.txt ;;
    *) procs_out=$out-procs ;;
esac

# snapshot prints "<comm label>|<cgroup label>" (the process that owns it) for every thread that is
# runnable or in uninterruptible sleep.
snapshot() {
    for st in "$proc"/[0-9]*/task/*/stat; do
        [ -e "$st" ] || continue
        owner=${st#"$proc"/}
        owner=${owner%%/*}
        { read -r line <"$st"; } 2>/dev/null && printf '%s %s\n' "$owner" "$line"
    done | awk '
        {
            pid = $1
            body = substr($0, length($1) + 2)
            p = index(body, " (")
            if (p == 0) next
            rest = substr(body, p + 2)
            q = 0
            for (i = length(rest) - 1; i > 0; i--) if (substr(rest, i, 2) == ") ") { q = i; break }
            if (q == 0) next
            st = substr(rest, q + 2, 1)
            if (st == "R" || st == "D") print pid, substr(rest, 1, q - 1)
        }' | while read -r pid name; do
        label=$(printf '%s' "$name" | tr ' |' '__')
        if [ "$label" = node ] && [ -r "$proc/$pid/cmdline" ]; then
            # shellcheck disable=SC2046
            set -- $(tr '\0' ' ' <"$proc/$pid/cmdline" 2>/dev/null)
            shift
            while [ $# -gt 0 ]; do
                case $1 in -*) shift ;; *) label=node:${1##*/}; break ;; esac
            done
        fi
        group=unknown
        if [ -r "$proc/$pid/cgroup" ]; then
            group=$(tail -n 1 "$proc/$pid/cgroup" 2>/dev/null | tr ' |' '__')
            group=${group##*/}
            group=$(printf '%s' "$group" | cut -c1-40)
            [ -n "$group" ] || group=root
        fi
        printf '%s|%s\n' "$label" "$group"
    done
}

# procs_row takes nsnap snapshots one second apart and appends the mean counts.
procs_row() {
    [ "$nsnap" -gt 0 ] || return 0
    all=$(mktemp "${TMPDIR:-/tmp}/loadsample.XXXXXX") || return 0
    k=0
    while [ "$k" -lt "$nsnap" ]; do
        snapshot >>"$all"
        k=$((k + 1))
        [ "$k" -lt "$nsnap" ] && sleep 1
    done
    ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    awk -F'|' -v n="$nsnap" -v ts="$ts" '
        { total++; c[$1]++; g[$2]++ }
        function top(arr, limit, label,   key, best, line, i) {
            line = ""
            for (i = 0; i < limit; i++) {
                best = ""
                for (key in arr) if (best == "" || arr[key] > arr[best] || (arr[key] == arr[best] && key < best)) best = key
                if (best == "") break
                line = line sprintf(" %s=%.1f", best, arr[best] / n)
                delete arr[best]
            }
            return line
        }
        END {
            printf "%s runnable=%.1f comm%s cgroup%s\n", ts, total / n, top(c, 10), top(g, 5)
        }' "$all" >>"$procs_out"
    rm -f "$all"
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

# The wait after a row is the interval less the time the snapshots took.
wait_s=$interval
[ "$nsnap" -le 0 ] || case $interval in
    *[!0-9]* | '') ;;
    *) [ "$interval" -gt "$nsnap" ] && wait_s=$((interval - nsnap + 1)) ;;
esac

pause "$warm"
while :; do
    row
    procs_row
    pause "$wait_s"
done
