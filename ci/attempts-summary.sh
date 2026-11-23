#!/bin/sh
# ci/attempts-summary.sh [--line] <timing.md> [cap minutes] — the infrastructure
# attempts of a build, read from the per-unit timing table the Jenkinsfile writes
# (ci-results/distributed/timing.md: | unit | result | attempt | node |
# queue (min) | run (min) | outcome |), for the published build summary.
#
# An attempt whose outcome is infra:* is an infrastructure attempt (a unit's
# infrastructure retries); infra:queue-wait is one that waited past the queue
# cap for a node (ci/queue-cap.sh) and is named "queue wait over <cap> min".
#
#   default  a Markdown section ("### Unit attempts": the count line, then a
#            table of every infrastructure attempt with its reason), which
#            ci/jenkins-report.sh appends to ci-summary/summary.md and so to the
#            CI summary page
#   --line   the count line alone ("3 infrastructure attempts (2 queue waits
#            over 30 min)", or "no infrastructure attempt"), which
#            ci/jenkins-report.sh writes to ci-summary/attempts.txt for the
#            Jenkinsfile's "Distributed" line on the build page
#
# No timing table (the build stopped before the report wrote one): prints
# nothing and exits 0. The cap is 30 minutes, the Jenkinsfile's
# QUEUE_CAP_MINUTES (ci/repocheck pins both). Plain sh and awk, no Groovy.
set -eu

mode=section
if [ "${1:-}" = "--line" ]; then
    mode=line
    shift
fi
timing=${1:?"usage: ci/attempts-summary.sh [--line] <timing.md> [cap minutes]"}
cap=${2:-30}
case "$cap" in
*[!0-9]* | "")
    echo "ci/attempts-summary.sh: cap '$cap' must be a whole number" >&2
    exit 2
    ;;
esac
[ -f "$timing" ] || exit 0

awk -F'|' -v mode="$mode" -v cap="$cap" '
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
# A row of the table: | unit | result | attempt | node | queue | run | outcome |
/^\|/ {
    unit = trim($2)
    if (unit == "unit" || unit ~ /^-+$/) next
    outcome = $8
    for (i = 9; i < NF; i++) outcome = outcome "|" $i
    outcome = trim(outcome)
    if (outcome !~ /^infra/) next
    infra++
    if (outcome == "infra:queue-wait") {
        waits++
        why = "queue wait over " cap " min"
    } else {
        why = "infrastructure"
    }
    rows[infra] = "| " unit " | " trim($4) " | " trim($5) " | " trim($6) " | " outcome " | " why " |"
}
END {
    if (infra == 0) {
        line = "no infrastructure attempt"
    } else {
        line = infra " infrastructure attempt" (infra == 1 ? "" : "s")
        if (waits > 0) line = line " (" waits " queue wait" (waits == 1 ? "" : "s") " over " cap " min)"
    }
    if (mode == "line") { print line; exit }
    print ""
    print "### Unit attempts"
    print ""
    if (infra == 0) { print line; exit }
    print line ": infrastructure failures, retried (a unit gives up after three)"
    print ""
    print "| unit | attempt | node | queue (min) | outcome | reason |"
    print "|------|---------|------|-------------|---------|--------|"
    for (i = 1; i <= infra; i++) print rows[i]
}
' "$timing"
