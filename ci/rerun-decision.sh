#!/bin/sh
# ci/rerun-decision.sh <attempt outcome> — whether the Jenkinsfile's runUnit
# starts another attempt of a unit after an attempt that ended this way.
# Exit 0: rerun. Exit 1: do not. Exit 2: an outcome this script does not know
# (the Jenkinsfile reads any non-zero status as "do not rerun"). One word on
# stdout, "rerun" or "no-rerun", for the build log.
#
#   infra:*   rerun: the attempt never judged the code (no node, agent lost,
#             Docker or the CI image unavailable, the command's process lost).
#             The Jenkinsfile still caps these at 3 infrastructure failures.
#   fail:*    no rerun: the unit's command exited non-zero (fail:<N>) or ran
#             out its own bound (fail:timeout). A rerun that passed would only
#             hide a flake, and a rerun of a timeout costs the same time again.
#   pass      no rerun: nothing to repeat.
#
# Plain sh, no Groovy, so ci/repocheck can run it against every outcome.
set -eu

outcome=${1:?"usage: ci/rerun-decision.sh <attempt outcome>"}
case "$outcome" in
infra:*)
    echo rerun
    exit 0
    ;;
fail:* | pass)
    echo no-rerun
    exit 1
    ;;
*)
    echo "no-rerun"
    echo "ci/rerun-decision.sh: unknown attempt outcome '$outcome'" >&2
    exit 2
    ;;
esac
