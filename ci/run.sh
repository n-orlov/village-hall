#!/bin/sh
# Run a command in a throwaway sibling container that has the project toolchain
# (Go, Node 24, the Playwright browsers and the gate tools). Works both inside a
# ralphd job container and directly on the host.
#
#   ci/run.sh go test ./...
#   ci/run.sh sh -c 'cd web && npm run test'
#   BOARD_GODOG_PATHS=initiatives.feature ci/run.sh go test ./features/ -run TestFeatures
#
# Invariants that matter:
#   - the bind source must be a HOST path, because siblings run on the host daemon;
#     inside a ralphd job that path is $RALPHD_HOST_WORKSPACE, not /workspace
#   - the sibling runs as the calling uid/gid, so it never leaves root-owned files
#   - the Go module/build cache and the npm cache live in named volumes shared
#     across runs, so warm invocations skip dependency downloads
#   - a sibling starts with a fresh environment, so every BOARD_* variable the
#     caller set in front of ci/run.sh is forwarded explicitly (see below), and
#     nothing else is: without that, selectors like BOARD_GODOG_PATHS were
#     silently dropped and the whole feature suite ran instead of the named files
#   - BOARD_CI_MEMORY=<size> (docker's units, e.g. 12g) puts a hard memory limit
#     on the sibling's cgroup, swap included (--memory = --memory-swap): a
#     process that runs away is killed by the cgroup's own OOM killer, alone,
#     instead of the host's global one picking anything on the agent. The
#     Jenkinsfile sets it on every full-mode mutation unit (SPEC 15.3), where
#     a mutated cli.test once grew to 54 GB. Containers the sibling starts
#     through the docker socket (Postgres testcontainers) are outside it.
#   - BOARD_CI_CPUS=<n> (docker's --cpus, e.g. 4) caps the sibling's CPU time:
#     the kernel throttles its threads at n CPUs, and a throttled thread is not
#     runnable, so it does not count in the host's load average. The Jenkinsfile
#     sets it on every Core unit (R6: load at most 12 on int21h-ws, 12 hardware
#     threads, where an uncapped Go build of a cold cache ran 25-30 threads).
#     Empty or unset: no cap.
set -eu

workspace=${RALPHD_HOST_WORKSPACE:-$PWD}
image=${BOARD_CI_IMAGE:-board-ci:local}
volume=${BOARD_CI_CACHE_VOLUME:-board-go-cache}
npm_volume=${BOARD_CI_NPM_VOLUME:-board-npm-cache}
memory=${BOARD_CI_MEMORY:-}
cpus=${BOARD_CI_CPUS:-}

if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "error: image $image not found; build it with:" >&2
    echo "  docker build -t $image -f ci/Dockerfile ci" >&2
    exit 1
fi

# Deliberately shared across runs: the caches are the whole point, so they are
# named after the toolchain rather than a run id. Remove one to force a cold build.
if ! docker volume inspect "$volume" >/dev/null 2>&1; then
    docker volume create --label board.cache=go "$volume" >/dev/null
fi
if ! docker volume inspect "$npm_volume" >/dev/null 2>&1; then
    docker volume create --label board.cache=npm "$npm_volume" >/dev/null
fi

# The sibling command is assembled by appending the docker prefix *after* the
# caller's own argv and then rotating that argv to the end, so the forwarded `env`
# assignments can sit between the image name and the command being run.
user_argc=$#

set -- "$@" docker run --rm \
    --user "$(id -u):$(id -g)" \
    --workdir /w \
    --mount "type=bind,src=$workspace,dst=/w" \
    --mount "type=volume,src=$volume,dst=/go-cache" \
    --mount "type=volume,src=$npm_volume,dst=/npm-cache" \
    ${memory:+--memory=$memory --memory-swap=$memory} \
    ${cpus:+--cpus=$cpus} \
    ${RALPHD_RUN_ID:+--label ralphd.run=$RALPHD_RUN_ID} \
    ${RALPHD_RUN_ID:+--label ralphd.role=sibling}

# The docker socket (the Postgres leg of the feature matrix): the sibling mounts
# the host daemon's socket, joins the socket's group so the non-root uid can use
# it, and resolves host.docker.internal to the docker host. Containers the
# features harness starts through testcontainers are siblings of this one, on
# the host daemon; their published ports are on the host, so the harness reaches
# them at host.docker.internal (BOARD_CI_DOCKER_HOST) and labels them as
# siblings with the run id (BOARD_CI_RUN_ID). The socket's HOST path is the same
# path the caller sees, since a ralphd job container mounts it at that path.
sock=${BOARD_CI_DOCKER_SOCK:-/var/run/docker.sock}
extra_env=""
if [ -S "$sock" ]; then
    sock_gid=$(stat -c %g "$sock" 2>/dev/null || stat -f %g "$sock")
    set -- "$@" \
        --mount "type=bind,src=$sock,dst=/var/run/docker.sock" \
        --group-add "$sock_gid" \
        --add-host host.docker.internal:host-gateway
    extra_env="BOARD_CI_DOCKER_HOST=host.docker.internal"
fi
if [ -n "${RALPHD_RUN_ID:-}" ]; then
    extra_env="$extra_env BOARD_CI_RUN_ID=$RALPHD_RUN_ID"
fi
set -- "$@" "$image"

# Forward every BOARD_* variable present in the caller's environment, as an `env`
# prefix on the command itself. This makes
#   BOARD_GODOG_PATHS=x ci/run.sh go test ./features/ -run TestFeatures
# behave exactly like the longhand form
#   ci/run.sh env BOARD_GODOG_PATHS=x go test ./features/ -run TestFeatures
# which keeps working unchanged. The BOARD_CI_* values above come first, so a
# caller's own value of the same name wins.
forwarded=$(env | sed -n 's/^\(BOARD_[A-Za-z0-9_]*\)=.*/\1/p' | sort -u)
if [ -n "$forwarded" ] || [ -n "$extra_env" ]; then
    set -- "$@" env
    for assignment in $extra_env; do
        set -- "$@" "$assignment"
    done
    for name in $forwarded; do
        eval "set -- \"\$@\" \"$name=\${$name}\""
    done
fi

rotated=0
while [ "$rotated" -lt "$user_argc" ]; do
    head=$1
    shift
    set -- "$@" "$head"
    rotated=$((rotated + 1))
done

exec "$@"
