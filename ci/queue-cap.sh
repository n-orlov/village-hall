#!/bin/sh
# ci/queue-cap.sh <seconds waited for a node> [cap minutes] — whether a unit's
# wait in the executor queue went past the cap. Exit 0: within the cap. Exit 1:
# past it, so the Jenkinsfile's runUnit fails that attempt as infrastructure
# (outcome infra:queue-wait, "queue wait" in the build summary), which keeps its
# infrastructure retries. Exit 2: a wait or cap that is not a number. One word on
# stdout, "within-cap" or "queue-wait", for the build log.
#
# The cap is 30 minutes. Waiting exactly the cap is within it.
# The Jenkinsfile's QUEUE_CAP_MINUTES must equal the default below
# (ci/repocheck pins both).
#
# Plain sh, no Groovy, so ci/repocheck can run it against every case.
set -eu

waited=${1:?"usage: ci/queue-cap.sh <seconds waited> [cap minutes]"}
cap=${2:-30}
case "$waited$cap" in
*[!0-9]* | "")
    echo "ci/queue-cap.sh: '$waited' and '$cap' must be whole numbers" >&2
    exit 2
    ;;
esac
if [ "$waited" -gt $((cap * 60)) ]; then
    echo queue-wait
    exit 1
fi
echo within-cap
