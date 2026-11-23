#!/bin/sh
# ci/attempt-id.sh <job name> <build number> <unit name> <attempt number> —
# the id of one unit attempt: the ralphd.run label the Jenkinsfile gives the
# attempt's sibling and Testcontainers containers, and the one its cleanup
# removes them by (exact ID, never a pattern). Prints
# jenkins-<job>-<build>-<unit>-<attempt>, job and unit lower-cased with every
# run of other characters turned into one '-'.
#
# The job is part of the id: build numbers repeat across jobs and branches
# (PR-1 #3 and PR-2 #3), and a cleanup by an id without the job once removed
# another branch's running E2E container. Same inputs, same id.
#
# Plain sh, no Groovy, so ci/repocheck can run it against real inputs. An empty
# argument is refused: an id with a missing part could match another job's.
set -eu

[ $# -eq 4 ] || {
    echo "usage: ci/attempt-id.sh <job name> <build number> <unit name> <attempt number>" >&2
    exit 2
}
for arg in "$@"; do
    [ -n "$arg" ] || {
        echo "ci/attempt-id.sh: an empty argument would give an id another job can share" >&2
        exit 2
    }
done

slug() { printf '%s' "$1" | tr 'A-Z' 'a-z' | sed 's/[^a-z0-9][^a-z0-9]*/-/g'; }

printf 'jenkins-%s-%s-%s-%s\n' "$(slug "$1")" "$2" "$(slug "$3")" "$4"
