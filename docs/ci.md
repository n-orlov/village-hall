# CI

This is the only CI document, for Village Hall (`vh`). CI-internal names keep their `board` spelling
(`BOARD_CI_*`, `BOARD_GODOG_*`, `BOARD_DB_LEG`, `BOARD_MUTATION_*`, `BOARD_IMAGE_*`, `board-ci:local`,
`board:ci`, the Jenkins job paths, labels and credential IDs): SPEC §13.1 lists what is kept. It grows with the pipeline; this section covers
the toolchain image and how every command runs in it.

## Toolchain image

`ci/Dockerfile` builds `board-ci:local`:

- Go on the 1.26.x line, Node 24, and the Playwright 1.64.1 browsers
  (chromium, firefox, webkit) under `/ms-playwright`;
- gotestsum, golangci-lint, govulncheck, gremlins, shellcheck and trivy.

Every `FROM` and every `COPY --from` image is pinned by `@sha256:` digest, and
the tool versions are pinned in the file. `ci/repocheck/dockerfiledigest_test.go`
fails on an unpinned one. Build it once by hand:

    docker build -t board-ci:local -f ci/Dockerfile ci

## Running a command: `ci/run.sh`

`ci/run.sh <command...>` runs the command in a throwaway sibling container of
the image above, on the host docker daemon:

- the bind source is `$RALPHD_HOST_WORKSPACE` (a HOST path), else `$PWD`;
- the sibling runs as the caller's `uid:gid`, so it leaves no root-owned files;
- two named volumes carry the caches: `board-go-cache` (`/go-cache`: Go module,
  build and golangci-lint caches) and `board-npm-cache` (`/npm-cache`);
- every `BOARD_*` variable in the caller's environment is forwarded, and no
  other variable is. `BOARD_CI_IMAGE`, `BOARD_CI_CACHE_VOLUME` and
  `BOARD_CI_NPM_VOLUME` override the image and the volume names;
- when the docker socket exists (`/var/run/docker.sock`, or
  `BOARD_CI_DOCKER_SOCK`) it is bind-mounted into the sibling, the sibling joins
  the socket's group so its non-root uid can use it, and `host.docker.internal`
  resolves to the docker host. See the next section.

`ci/repocheck/runsh_test.go` runs the script against a stub `docker` and checks
that argv.

## The database matrix and the docker socket

The scenarios in `features/` run on two database legs, chosen by
`BOARD_DB_LEG=sqlite|postgres` (SQLite when unset):

    BOARD_GODOG_PATHS=health.feature BOARD_DB_LEG=postgres ci/run.sh go test -count=1 ./features/ -run TestFeatures

The Postgres leg starts a Postgres 16 container (pinned by digest in
`features/postgres_test.go`) with testcontainers-go through the docker socket
`ci/run.sh` mounted, so the container is a sibling of the toolchain container on
the host daemon, not a child of it:

- it publishes its port on the host, and the harness reaches it at
  `host.docker.internal` (`BOARD_CI_DOCKER_HOST`, which `ci/run.sh` sets while
  the socket is mounted);
- it carries the labels `ralphd.role=sibling`, `board.ci=postgres` and, when the
  caller runs inside a ralphd job, `ralphd.run=<run id>` (`BOARD_CI_RUN_ID`), so
  the job that owns the run can reap a leaked one without matching anything else;
- the testcontainers reaper is off (`TESTCONTAINERS_RYUK_DISABLED`): the harness
  terminates each container it started when the run, or the scenario that owns
  it, ends;
- one container serves the whole run, with a fresh empty database per scenario.
  A scenario tagged `@dedicated-database` gets a container of its own, so that
  `the database is stopped` cannot disturb another scenario. A scenario tagged
  `@postgres` runs on the Postgres leg only (the SQLite leg runs `~@postgres`).

`ci/suite.sh` runs the `features/` pass once per leg of `BOARD_CI_DB_LEGS`
(default `sqlite postgres`; SQLite is always first and keeps the file names it
had before the matrix). Each leg runs once, with no rerun of a failed scenario, and
the other legs write files named for them: `features-<leg>.log` and
`junit-features-<leg>-gotestsum.xml`. Both legs
write a merged JUnit, `junit-merged/junit-features-<leg>.xml`, kept apart so that
one scenario run on both legs is never read as a retry of itself, and
`ci/legcompare` reads them: a scenario green on one leg and red on another is
written to `leg-divergence.txt` and fails the suite. A scenario red on both legs
fails its legs, not the comparison, and one that only some legs run is not
compared. `ci/summary.sh` counts every leg. Allure covers the SQLite leg only:
the same scenario on a second leg would share a history id with it and read as
its retry.

`ci/suitecheck/legfixture_test.go` runs the real `ci/suite.sh` against
`testdata/legfixture/`, a `TestFeatures` that reads `BOARD_DB_LEG` and is red on
the leg `LEGFIXTURE_RED_ON` names, and requires the suite to fail and name the
scenario.

## Disposable-clone measurement: `ci/review.sh`

`ci/review.sh` clones the repository into the git-ignored `.review-clone/` and,
through `ci/run.sh`, asserts that the module path is `github.com/n-orlov/village-hall`
and that `go list ./internal/...` resolves only to directories under the clone,
never the original checkout. With no argument (or `--smoke`) it stops there and
exits 0; any other arguments are passed to `go test` inside the same clone.
The clone is of the committed HEAD, so commit before running it.

## The Jenkinsfile

`Jenkinsfile` is the multibranch job `board/ci`
(`https://jenkins.wisent-owl.ts.net/job/board/job/ci/`; `@Library('homelab')`,
`agent none`, `disableConcurrentBuilds(abortPrevious: supersedable())`, a 300-minute pipeline timeout as a backstop). The
header comment of the file is the authoritative description. Every test stage is
a **unit**: its own parallel branch, with its own retry policy, that checks the
build's commit out into its own workspace on the one agent every unit uses
(`int21h-ws`, reached through the single label constant `AMD`, `board-test &&
amd64 && docker`, with 4 executors; no unit asks for an arm64 or any other
label), builds `board-ci:local` (`ci/jenkins-image.sh`, cached by the Docker
daemon; cache volumes `board-go-cache-jenkins` and `board-npm-cache-jenkins`,
each chowned to the agent's uid once), runs, and removes the sibling and
Testcontainers containers its attempt labelled (`ralphd.run=<attempt id>`, by
exact ID; the id comes from `ci/attempt-id.sh` (job, build number, unit and attempt, tested by `ci/repocheck/attemptid_test.go`), so a build of another branch with the same number never matches). The Mutation units are no exception (each parallel unit has its own attempt id, so none reaps another's containers): `ci/mutation.sh` scrubs every `BOARD_*`
variable but hands two on to gremlins: `BOARD_CI_RUN_ID` (the attempt id `ci/run.sh`
forwards from `RALPHD_RUN_ID`, so any container a mutant's test run labels is
reaped by the cleanup) and `BOARD_DB_LEG=sqlite`. Mutation runs the SQLite leg
only: gremlins' covered run and every mutant's test run for `internal/server`,
`internal/store` and `internal/feed` start no Postgres container (one per mutant
was a large share of a mutant's time): a test of those packages that exists only
for Postgres (two servers or two feeds on one Postgres database) runs as a
`postgres` subtest only when `BOARD_DB_LEG` is unset or `postgres`, and their
Postgres helpers fail a test that asks for a container on `BOARD_DB_LEG=sqlite`. The Postgres leg of those packages, and
both legs of `features/TestFeatures`, run in the Suite unit (`ci/suite.sh`),
where the Go matrix still runs both legs. An infrastructure failure (agent lost, Docker unavailable, a wait
past the 30-minute queue cap) gets another attempt, up to three. A failing command is not
rerun and nothing marks the build UNSTABLE for a flake (an attempt outcome
`fail:<exit status>` fails the unit at once): `runUnit` asks
`ci/rerun-decision.sh <outcome>`, which answers rerun only for `infra:*`
(`fail:1` and `fail:timeout` no rerun, `infra:no-node` and `infra:agent-lost`
rerun; an outcome classified off a node, where nothing can run the script, is
rerun only when it starts with `infra`). A flaky list (`ci-results/flaky-*.txt`)
or a leg-divergence list (`ci-results/leg-divergence.txt`) with a line in it
fails the build: the report runs `ci/flakygate.sh ci-results` over every unit's
unstashed results and sets the build to FAILURE (no list, or only empty ones,
passes). A unit that runs on a
node and hits its own time bound (`timeout(time: u.minutes)`) is a failure, not
an infrastructure failure, and is not retried: the attempt's outcome is
`fail:timeout` and the unit fails at once (three 45-minute attempts of a Suite
that cannot fit used to cost a push build over two hours). Only a queue wait keeps
its retries: a unit's wait for a node is capped at 30 minutes (`QUEUE_CAP_MINUTES`
in the `Jenkinsfile`, the 30-minute queue cap; it replaced a timeout of the unit's
bound plus 45 minutes, so a Suite could wait 90 minutes). `runUnit` runs each
attempt's node block under `queueCapped`, a watcher that cancels a node request
still queued at the cap, and asks `ci/queue-cap.sh <seconds waited> <cap minutes>`
once the node is taken (exit 1 past the cap, `queue-wait` on stdout, exit 0 and
`within-cap` otherwise; plain sh, tested by `ci/repocheck`). Either way the
attempt's outcome is `infra:queue-wait`, and the attempt keeps its infrastructure
retries (up to three). The published summaries name it: `ci/attempts-summary.sh
<timing.md>` (plain sh and awk, tested by `ci/suitecheck`) reads the timing table, and
`ci/jenkins-report.sh` ends the CI summary page (`ci-summary/summary.md` and
`index.html`) with its "Unit attempts" section, a table of every infrastructure
attempt (unit, attempt, node, queue minutes, outcome) whose reason reads "queue wait
over 30 min" for a queue wait, and writes its count line (`--line`: "3 infrastructure
attempts (2 queue waits over 30 min)", or "no infrastructure attempt") to
`ci-summary/attempts.txt`, which the build page's "Distributed" line carries. The cap
covers the wait only: the unit's own bound starts with its node. The build page carries a per-unit timing table
(`ci-results/distributed/timing.md`).

### Unit runs: `ci/unitrun.sh`

The Suite and E2E units and the three full mutation units run their command
through `unitSh` in the `Jenkinsfile`, which calls `ci/unitrun.sh <unit name>
<bound seconds> <results dir> -- <command>`. The script runs the command under
`timeout(1)` with a bound a little inside the attempt's own limit (`runUnit`
passes the limit less the time checkout and the image build used, less 2
minutes; the bound is only ever lower than the unit's limit). A command that
outlives it is killed with its process group (TERM, then KILL 15 s later), the
sibling containers of the attempt are removed by exact ID (label
`ralphd.run=<attempt id>` and `ralphd.role=sibling`, never the job's own
container), and the script exits 124, which `runUnit` takes as the unit's own
timeout: `fail:timeout`, a failure that is not retried. Any other exit status
passes through.

Nothing is deleted: every `junit-*.xml`, log, `flaky-*.txt` and finished
mutation report the command wrote before it failed or was cut stays in
`ci-results`, where the unit's stash and the cut-off archive take it, and
`unit-kept-<unit>.txt` lists them. On a failure or a cut the script writes
`unit-failure-<unit>.txt` and prints the same text: how the unit ended (`cut at
N min`, or `failed with exit status N`), then every failing test read from the
results directory's `junit-*.xml`, one line each, grouped by file (the test's
classname and name: spec file and title for Playwright and Vitest, package and
test for Go, feature file and scenario for Godog). `unitSh` echoes it to the
console and appends its first lines to the build description. A pass writes no
summary. `ci/repocheck/unitrun_test.go` runs the script against a stub command
that writes a failing junit case and finished reports and is killed by a short
bound, and fails for a plain pass-through.

### Load samples: `ci/loadsample.sh`

R6 chooses the Playwright worker count from, and judges its load ceiling (the
1-minute load average at most 12 on every sample) on, load measured during a
push build. The E2E unit (`sample: true` in the `Jenkinsfile`) runs its command
under `ci/loadsample.sh ci-results/load-samples.txt -- ci/unitrun.sh ...`, on
int21h-ws itself, outside the CI container. The script appends one row per
interval (60 s by default, `-i`) to the file: a UTC timestamp, `load1=` (the
first field of `/proc/loadavg`), `busy=` and `iowait=` (percent of all CPU time
between two reads of the `cpu` line of `/proc/stat`). The first row is written 1 s
(`-w`) after the start and the command starts once it is on disk; a final row is
written when the command ends, and the script exits with the command's exit
status. Without a command it samples until TERM or INT, writes its last row and
exits 0. The report archives `ci-results/load-samples.txt`
(`ci-results/**/*.txt`) with the build's other artifacts. The file covers the
E2E unit's command, which runs beside Suite, Image and Allure smoke, so it is
the load of the whole Core phase from the point E2E starts.
`ci/repocheck/loadsample_test.go` runs the script against a fake proc directory
at a short interval (rows, fields, the exact percentages, a clean stop on TERM,
the command's exit status) and checks the Jenkinsfile runs the E2E unit under it.

### Playwright worker count

The count is set in one place, `E2E_WORKERS` in the `Jenkinsfile` (2; at most 3
on int21h-ws, R6). The `Jenkinsfile` exports it as `BOARD_E2E_WORKERS`,
`ci/suite.sh` (header) refuses anything but a whole number from 1 to 3 and hands
it to Playwright as `E2E_WORKERS`, and `e2e/playwright.config.ts` reads it for
`workers`: the config has no number of its own and a run with none (a local one)
uses one worker. No shard, no `--workers` flag. `ci/suitecheck`
(`TestE2EWorkerCountHasOneSourceAndAtMostThree`,
`TestE2EWorkerCountProblemFlagsDoctoredFiles` and
`TestSuiteHandsTheWorkerCountToPlaywrightAndRefusesMoreThanThree`) pins the
single source and the limit, and fails on a count of 4 and on a config that
hard-codes a number.

Why 2, from the load samples of two earlier one-worker push builds of `main`
(#288 and #291, the files `ci/loadsample.sh` archived, above): while Suite had
ended and the E2E unit ran alone (#291, 15:42 to 16:01 UTC) the 1-minute load was
1.3 to 2.7 and CPU busy 8 to 24 % of 28 threads, so a worker costs about 2, and a
second one fits. Over the unit's last stretch, at the same one worker, the load
ran 7 to 11 and busy about 70 % (#288 12:30 to 12:47, #291 16:03 to 16:11), so
that stretch has about 1 to spare under the ceiling of 12 and a third worker would
not keep to it. The first minutes, when Suite, Image and E2E all start, peaked at
17.2 (#288) and 23.5 (#291) at one worker: that start-up burst does not depend on
the count and is judged by the load check (task 020) and the sweep, which can
bring the count back to 1 if two cross 12.

### What Quality and the report read from a failed or cut unit

Quality runs once Suite, E2E and Image have ended, whatever their outcome, and
reads the units' stashes together. A unit that failed or was cut by its bound
has a stash (`unitSh` and `runUnit` stash `ci-results` after the command,
failed or not). An attempt that hit the Jenkins limit or lost its agent before
its stash also keeps its results: it writes `ci-results/unit-cut-<unit>.txt`,
stashes `ci-results` and archives the logs and JUnit files. Only a unit that
never ran at all (no node) leaves no stash, and Quality names that unit and its
attempts instead of failing on a missing stash.

`ci/quality.sh` passes the results directory to `ci/qualitycheck` as
`-unit-results`, which adds a **units** section, first in the report, whenever
a unit left a failure to report, and the section fails Quality (the other gates
then judge results that are not a whole run). It prints each
`unit-failure-<unit>.txt` as the unit wrote it (`unit E2E: cut at N min ...`
and the failing tests), and for a unit with no summary (a `unit-cut-<unit>.txt`
note, or a failing case in its JUnit files) it names the unit and reads the
failing cases from `junit-playwright*.xml` (E2E) or `junit-vitest*.xml`,
`junit-go*.xml` and `junit-features*.xml` (Suite). A JUnit file cut off
mid-write yields the cases before the cut. The CI summary page shows the section
like any gate.

Playwright writes its own JUnit file only when its run ends, so a cut E2E would
leave none. `e2e/partial-junit-reporter.ts` rewrites
`ci-results/junit-playwright-partial.xml` after every failed test and at most
every 20 s otherwise (through a rename), and `ci/suite.sh` removes it once the
complete `junit-playwright.xml` exists. `ci/qualitycheck/units_test.go` cuts a
stub E2E unit with `ci/unitrun.sh`, runs `ci/quality.sh` over what it left and
asserts the unit, the cut and the failing test are named.

The pipeline runs Core, then Stability and Mutation side by side, then Perf; the
units of a phase run in parallel, and a phase runs only if every earlier one
passed (UNSTABLE counts as passed; Perf needs only the Stability unit to pass).
A nightly build takes about an hour with incremental mutation and about 130
minutes with full mutation, Core about 33 of them:

| phase | units | runs |
|---|---|---|
| Core | Lint (`ci/run.sh ci/lint.sh`), Suite (`BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh`: everything but Playwright), E2E (`BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh`: Playwright alone), Image (`ci/image-build.sh` then `ci/image-check.sh`, then the release dry run `ci/release-check.sh`), Allure smoke (the Godog formatter fixture, then `ci/allure-smoke.sh`), and Quality (`ci/run.sh ci/quality.sh ci-results ci/quality.json --mutation-deferred`, then `ci/run.sh go run ./ci/pkgtime`), which waits for Suite (its coverage), E2E (Playwright's results) and Image (the tar) without holding an executor | every build |
| Stability | one unit (one executor) running `ci/stability.sh 1` three times side by side, each run in its own clone of the checkout and its own stage (`Stability 1` to `3`), beside Mutation | `EXTENDED=true` or the nightly timer |
| Mutation | one unit (diff), three units and a gate (full), or a plan, the units it gives work and a gate (incremental), beside Stability (below) | `MUTATION=true` or `MUTATION_FULL=true` or the nightly of `main` |
| Perf | the Perf unit (`ci/run.sh ci/perf.sh ci-results`, 30 minutes), alone, when the Stability unit passed | `EXTENDED=true` or the nightly timer |

Three booleanParams select what runs: `EXTENDED`, `MUTATION` and
`MUTATION_FULL`; the choice `MUTATION_MODE` (`incremental`, the default, or
`full`) sets the nightly's mutation mode. The nightly timer (`17 0 * * *`, clear of the deck, ralphd and s3-browser nightlies that share int21h-ws from 02:17; on
`main` only; every other branch has no timer) counts as `EXTENDED` plus the
nightly's mutation, and so does `EXTENDED=true` on `main`.

- **Core** runs on every build, a push build of `main` included. `EXTENDED=true`
  and the nightly timer also add `-race` (the Suite's limit goes from 45 to 120
  minutes) and fresh vulnerability databases to Core.
- **E2E** is the Playwright suite in a unit of its own, in parallel with Suite,
  Lint, Image and Allure smoke on `int21h-ws` (operator ruling 001-WAVE): the
  Suite unit asks `ci/suite.sh` to skip the step (`BOARD_CI_PLAYWRIGHT=skip`) and
  E2E asks it for only that step (`BOARD_CI_PLAYWRIGHT=only`: `npm ci` and the web
  build, then Playwright once, with no repeat of a failed run; no Vitest, no Go
  pass). E2E's bound is 120 minutes on push and `EXTENDED` builds alike; Suite's
  stays 45 minutes (120 `EXTENDED`). Nothing else about Playwright changed: one
  worker, no `fullyParallel`, no shards, no added retry, no raised per-test
  timeout, no raised thread budget, and every test runs on every push build.
  E2E stashes `res-e2e` (`junit-playwright.xml`, the `playwright/` output,
  `playwright.log`), which Quality unstashes beside
  `res-suite` and the report reads with the others. A unit that left no stash (it
  timed out, or never got a node) is named by Quality, with its result and
  attempts, instead of Quality failing on `No such saved stash`.
- **Stability** is the extended lane: `EXTENDED=true` or the nightly timer.
  Its one unit holds a single executor and runs the three `ci/stability.sh 1`
  runs side by side (about 11 minutes), each in a local clone of the checkout
  (`stability-<i>`, at the build's commit, with the checkout's `origin` URL and
  refs) with its own `TMPDIR` and log (`ci-results/stability/run-<i>/`); their
  Postgres testcontainers publish random host ports, so the runs share
  nothing but the Go and npm caches. The third runs at `go test -parallel 1`
  (`BOARD_STABILITY_PARALLEL=1`): there a `t.Parallel` subtest waits behind
  every other parallel test of its package, so a test that takes a wall-clock
  instant before it queues sees it pass (the race in `askwait_test.go` that
  failed #240's `streams+cli` mutation unit, which runs at `-parallel 2` under
  load, and failed every `-parallel 1` run of `internal/server`). Any run failing fails the unit, which is
  not rerun. Until #238 each run was a unit of its own, and the
  three executors they held kept the mutation units waiting.
  - The **Perf** unit runs the SPEC 14 harness (`ci/perf.sh`, build tag `perf`) after
    the stability and mutation units, alone on the agent so their load is not part of what it
    measures, under a 30-minute bound (about 4 minutes on `int21h-ws`). It seeds 1 M
    events, 50 k notes and 200 live leases, starts the real `vh` binary and checks the
    eight targets (idle RSS, cold start, `/now` and `/attention` p99, lease renew p99,
    200 req/s for 60 s, 500 SSE clients, `vh now` and `vh version` p95). A breached or
    unmeasured gate fails the unit and the build; `ci-results/perf.json` (every measured
    figure and its gate) is archived with the build. The targets are verified on the
    agent; no baseline is committed. A gate the agent misses is cured in the code or
    petitioned with the `perf.json` numbers, never loosened.
- **Mutation** is gated on `MUTATION`, `MUTATION_FULL` or the nightly of `main`
  and on nothing else: `EXTENDED=true` on another branch runs no mutation.
  - *diff* (`MUTATION=true`): only the lines changed since a base, in one
    45-minute unit (`BOARD_MUTATION_DIFF=<base>`). The base is the merge base
    with `origin/<CHANGE_TARGET, or main>` (fetched into the workspace first,
    because the multibranch checkout fetches only the built branch), and `HEAD~1`
    on `main` itself, where the merge base would be `HEAD`. A package with no
    mutant passes; one with mutants keeps the 80 efficacy floor; Stryker runs
    only if `web/src/lib` changed.
  - *full* (`MUTATION_FULL=true`, `MUTATION_MODE=full`, Sunday's nightly, or a
    nightly that cannot be incremental): every package
    plus Stryker in three parallel units (`FULL_MUTATION` in the Jenkinsfile), each
    `ci/run.sh ci/mutation.sh ci-results/mutation-units/<dir> ci/quality.json
    <lane>,<lane>` over its own lanes of `mutation_go.lanes` (every lane in exactly
    one unit, Stryker with lane `base`), then the Mutation gate unit,
    `ci/mutation-merge.sh`, which merges their reports into `ci-results/mutation`
    and judges every package once. The three units start together beside the
    Stability unit (1 + 3 of the 4 executors), each `internal/server` part in a
    unit of its own. Each runs `nproc / 3` gremlins workers at GOMAXPROCS 2 per
    worker (`BOARD_MUTATION_WORKERS`, `BOARD_MUTATION_TEST_CPU`; `nproc / 2`
    when at most two units run), 12 workers on `int21h-ws`, at `nice 10`, under
    a 110-minute limit and a single attempt; Stryker, at the end of the unit
    with lane `base`, runs `nproc / 2` test processes at `nice 10`
    (`BOARD_MUTATION_STRYKER_CONCURRENCY`, read by `web/stryker.config.mjs` as
    `STRYKER_CONCURRENCY`; Stryker's own default is one per CPU less one).
    A worker is busy only about half the time (compiling its mutant, waiting
    for its Postgres testcontainers), so 12 workers keep about `nproc` threads
    busy: #242 ran the CPU 81 to 99 % busy (load 20 to 30, peak 44.6) and its
    units took 72 to 85 minutes. #244 tried `nproc / (2 × units)`, 2 workers
    per unit: the CPU fell to 30 to 45 % busy (load 5 to 8), each package of
    the streams+cli unit took about 1.4 times as long, and that unit hit its
    110-minute limit, so the split went back to `nproc / 3`.
    Every mutation unit (diff mode's too) runs under a hard memory cap of 12 GB
    (`MUTATION_MEMORY` in the Jenkinsfile: `ci/run.sh`'s `BOARD_CI_MEMORY`,
    `docker run --memory=12g --memory-swap=12g`). A mutant that allocates
    without bound is then OOM-killed inside its unit's cgroup, alone: `go test`
    reports `signal: killed` and exits 1, which gremlins counts as KILLED, as
    it would any failing test. Without the cap a mutated `cli.test` of #242
    grew to 54 GB and set off the host's global OOM killer, about 20 times
    since 10 Nov 2026, which may kill anything on `int21h-ws` (a ralphd run,
    the Jenkins agent). A normal unit stays far below it: a probe of `internal/cli` with 2 workers
    peaked near 5.5 GB, nearly all of it page cache of gremlins' work copies
    (anonymous memory under 0.5 GB), and its runaway mutant hit the cap within
    15 s and was killed alone (`CONSTRAINT_MEMCG`) while gremlins went on; in
    #244 a runaway `cli.test` (12.3 GB anonymous) was killed the same way, with
    no global OOM on the host. The
    Postgres testcontainers a unit starts through the docker socket are
    outside its cgroup. `GOMEMLIMIT` is not set: it is a soft limit that makes
    a runaway collect harder rather than stop.
    `ci/mutation.sh` caps one mutant at about 5 minutes (its header): #233's
    single full unit spent two of its four hours on seven hanging
    `internal/server` mutants. #235 (four units, three of them waiting for the
    three stability units) took 128 minutes in all.
  - *incremental* (the nightly's default): the reference is the last
    successful build of this job whose description says `mutation(full)`. The
    Mutation plan unit copies its archived reports, Stryker report and commit
    (`ci-results/commit.txt`) and runs `ci/mutation-plan.sh`: every unit that
    no change since that commit can reach reuses its report, and the full-mode
    units run only the stale ones, each named on its own in the lane list (a
    unit with nothing stale is skipped). `ci/mutationgate -stale <ref>` decides:
    a unit is stale when a changed file lies in its package's directory (tests
    and testdata included), or is a non-test file of a module package its
    package or its tests import (`go list -deps -test`); a split package's parts
    go stale together. Stryker runs again only when `web/` changed. The gate
    then merges the units' reports with the reused ones
    (`ci-results/mutation-ref`), so every package is judged exactly once by
    this build's thresholds. It runs full instead on Sunday's nightly, with no
    reference, when the plan fails, or when a shared input changed (`go.mod`,
    `go.sum`, `ci/Dockerfile`, `ci/run.sh`, `ci/mutation.sh`, or
    `mutation_go`'s packages, scopes or parts). The build's description says
    `mutation(incremental: N run, M reused from #K)`, or `mutation(full)
    [<reason>]` when it fell back.

A red Lint, Suite, E2E, Image, Allure smoke, Quality, Stability, Perf or Mutation unit
fails the build. Within a phase the other units still finish, and a later phase
does not start after a failure.

**Allure smoke.** `features/allure_formatter_test.go` is Village Hall's Godog formatter
for Allure 2 (selected by `BOARD_GODOG_ALLURE=<absolute results dir>`, which
`ci/suite.sh` sets for the SQLite leg's `features/` run). `features/allure_formatter_fixture_test.go` runs a fixture
feature through it (a pass with an issue link, a failure with attachments, a skip
and a retried scenario). The unit runs that test with `BOARD_ALLURE_FIXTURE_OUT`
set, then `ci/allure-smoke.sh` runs the pinned Allure CLI over the results and
fails unless the report carries the fixture's steps and its `@gh-61` link.

**Quality and Mutation.** `ci/mutation.sh` writes the reports that `mutation-go`
and `stryker` read, in the Mutation phase, which comes after Core (Quality
included) and runs only when `MUTATION`, `MUTATION_FULL` or the nightly asks
for it. So the Quality unit passes `--mutation-deferred` as `ci/quality.sh`'s third
argument: those two rows print "deferred to the Mutation unit" instead of
reading reports that cannot exist yet. The Mutation unit's `ci/mutation.sh`
judges the same reports against the same `ci/quality.json` thresholds (efficacy,
break, per-module break, no mutant fails) and fails the build below them.
Without the flag (`ci/run.sh ci/quality.sh`, the local recipe) the rows read
`ci-results/mutation/` and fail when it is missing. Every other gate runs in the
Quality unit on every build.

**The quality report format.** Every gate prints a section `=== <name> gate ===`
(its own human-readable body) that ends in exactly one machine-readable line,
`gate verdict: passed`, `gate verdict: failed` or `gate verdict: deferred`.
`ci/qualitycheck` derives it from the gate's result alone: a deferred gate (the
two mutation rows under `--mutation-deferred`) stays ok, so the exit status is
unchanged, but it is never reported as passed. The CI summary page
(`ci/jenkins-report.sh`) reads that line and nothing else, never a gate's
wording: `passed` is a green badge, `failed` a red one with the section open,
`deferred` a neutral grey one, and a section with no verdict line reads
`failed`. The quality tile reads `passed` when the report has at least one gate
and none failed (a deferred gate is not a failure), `failed` otherwise (an
empty or missing report included), so it agrees with `ci/quality.sh`'s exit
status. A deferred `mutation-go` or `stryker`
section says "not run in this build" when the build has no Mutation unit
results. When it has them (`ci-results/mutation/`, unstashed by the report step)
the section shows that unit's verdict for the gate, from
`ci-results/mutation/unit-verdict.txt` (`mutation-go: passed|failed`,
`stryker: passed|failed|not run`, written by `ci/mutation.sh` next to its
reports), and links to the archived reports.

**The image.** The root `Dockerfile` is multi-stage (the Svelte build, a static
`CGO_ENABLED=0` Go build, then `FROM scratch` with the binary, the CA
certificates, the time zone database embedded in the binary with
`-tags timetzdata`, `/data` and uid 65532), every `FROM` pinned by digest. Both
builder stages are `FROM --platform=$BUILDPLATFORM` and the Go build takes
`GOOS=$TARGETOS GOARCH=$TARGETARCH`, so linux/amd64 and linux/arm64 are built by
cross-compiling on the agent, without emulation: the final stage only `COPY`s.
`ci/image-build.sh` (on the agent, outside `ci/run.sh`: it needs the docker CLI)
does the build. The agent's docker has no buildx, so the script copies the buildx
plugin out of a digest-pinned `docker/buildx-bin` image into a throwaway docker
config, starts a digest-pinned `moby/buildkit` container as a `docker-container`
builder (removed on exit), builds both platforms into one OCI layout
(`ci-results/board-image-oci.tar`), builds amd64 again from the builder's cache and
loads it as `board:ci`, and saves it as `ci-results/board-image.tar`, which the
`trivy-image` and `sbom-image` gates scan. `ci/image-check.sh` then inspects what
was built: `go run ./ci/imagecheck` (through `ci/run.sh`) requires a manifest for
each platform, a non-root user, `/vh` as the entrypoint, the OCI title `Village Hall`,
at most 30 MB of compressed layers, and an ELF `/vh` whose machine is that
platform's (so an arm64 manifest holding the build host's binary fails);
`docker run board:ci version` runs the loaded image; the `imagedeploy` test in
`ci/imagecheck` (through `ci/run.sh`, JUnit in `ci-results/junit-image-deployed.xml`)
starts the image the way the deployment does (`serve`, `BOARD_*` variables only, a
read-only root, `tmpfs /tmp`, a non-root uid, no capabilities, OIDC on the mock
provider, `/data` holding a SQLite file at the 0315466 schema) and checks `/healthz`,
the existing `brd_` key, the existing `board_session` cookie and the rows; a container
of it must answer `GET /healthz` with 200. Both run in the Image unit of the Core
phase, whose tar the Quality unit then scans with `ci/quality.sh`.

The Image unit then runs the release dry run, `ci/release-check.sh` (below, "Release build"): it
builds every release asset at the commit being built and asserts them, and publishes nothing. It builds
no image of its own: the root `Dockerfile` is built once per architecture in the unit, by its one
`ci/image-build.sh`, and the dry run reuses that build's tars, so the image the release check
inspects is the image `ci/image-check.sh` just checked.
`ci/repocheck` fails when the final stage is not scratch or
distroless static, has a `RUN`, runs as root or sets no user, lacks the CA
certificates or tzdata, when a builder `FROM` lacks `--platform=$BUILDPLATFORM`, or
when the Go build does not cross-compile. Nothing is pushed: the registry push
belongs to the release job ("Release", below).

**Reports.** `post { always }` publishes the JUnit, Allure and CI summary reports,
declares the README badges, adds the build page summary line, and archives
`ci-results/` (the gremlins reports in `ci-results/mutation/` and the CycloneDX
SBOMs `ci-results/*.cdx.json` included). A fully green `main` build (checked in `post { always }`: `SUCCESS`, so an
UNSTABLE or failed build never qualifies) archives `ci-results/release-candidate.json`. `notifyTelegram` runs
last, in `post { cleanup }`.

- **Build page summary line** (badge plugin `addSummary`): pass/fail/skip/flaky
  counts and unit coverage, with links to the Allure and CI summary reports.
- **README badges** (`embeddable-build-status`): `build` (the plugin's own),
  `tests`, `coverage` and `quality`, which the Jenkinsfile declares on every
  build ("not run" until the build fills them in), served from
  `https://int21h.mooo.com/jenkins-static/board/ci/main/<id>.svg`. The README
  links each to the `main` job the way deck's do: `build` to `lastCompletedBuild/`,
  `tests` to `lastCompletedBuild/Allure/`, `coverage` and `quality` to
  `lastCompletedBuild/CI_20summary/` (the CI summary report), all under
  `https://jenkins.wisent-owl.ts.net/job/board/job/ci/job/main/`.

**Superseded builds (R8).** A newer push build of a branch stops the branch's older build, so a
branch never runs two builds at once: the job option is `disableConcurrentBuilds(abortPrevious:
supersedable())`, and `supersedable()` is true only while the running build is a push build (its
causes are a branch push event or branch indexing, it has no timer cause, and `EXTENDED`, `MUTATION`
and `MUTATION_FULL` are all false). Jenkins reads the option from the build that is running, so a push
queued behind main's nightly (the timer) or a parameterized build finds it false and waits: neither is
ever stopped by a push. A manual build (no push cause) is not stopped either. A superseded build ends
ABORTED, not FAILURE or NOT_BUILT. Jenkins stops it with the result NOT_BUILT and a `CancelledCause`
("Superseded by #N"); a build still running a few seconds later has its steps terminated, and is then
killed. The pipeline knows the stop by that cause or by a newer build of the branch existing
(`isSuperseded`: under `disableConcurrentBuilds` only abortPrevious starts one while a build runs).
`classify` lets it propagate (no attempt is retried), an attempt it cut short is neither rerun nor
judged, and the attempt that sees it, or `post { always }`, sets the result ABORTED (`endSuperseded`;
ABORTED is the worst result, so nothing that runs after it lowers it, and the build's description says
"superseded by a newer build of this branch"). Each attempt's `finally` removes its containers by the
attempt id; the superseded build then skips the report, so it ends at that point instead of being
killed and frees its executor for the newer build, which reports the branch. A report takes the Allure
history from the newest completed build that is neither ABORTED nor NOT_BUILT (`historyBuild`), so a
superseded build does not break it. The pipeline never reads the interruption's result: the script
sandbox refuses `FlowInterruptedException.getResult`. A queued nightly or parameterized build does stop
a push build that was running before it. `ci/repocheck/supersede_test.go` pins the option, the three
exclusions and the ABORTED end against doctored copies.

`ci/repocheck/jenkinsfile_test.go` pins all of it: Core, then Stability and
Mutation side by side, then Perf, with the Core units and Quality's wait for
Suite, E2E and Image (E2E: Playwright alone, 120 minutes; Suite: 45, 120 `EXTENDED`; a unit's own timeout is a failure, not retried, a wait with no node stays infra); the Perf unit (30 minutes, after the stability and mutation units, `perf.json` archived); the job shape (the one `AMD` label for every `node()`, the three
booleanParams, `disableConcurrentBuilds(abortPrevious: supersedable())`, the 300-minute timeout, the
per-attempt container cleanup by exact ID); the nightly lane (cron `17 0 * * *`
on `main` only); the Quality unit; the three Mutation modes (diff: one 45-minute
unit over the lines changed since the merge base, `HEAD~1` on `main`; full: three
110-minute single-attempt units whose lanes cover every lane exactly once, Stryker
included, and the gate over all of them; incremental: the reference search, the
plan, the selected units and the gate over them and the reused reports) and that
Mutation runs only for `MUTATION`, `MUTATION_FULL` or the nightly of `main`; that the job
never releases; the reports, the badges and `notifyTelegram`. Each property has
a case that feeds the check a copy of the Jenkinsfile with it removed or
altered, so a test fails if it is lost. `ci/repocheck/lintgate_test.go` pins that
Lint is the first Core unit and runs `ci/lint.sh`, and that a red unit fails the
build.

## Release

`Jenkinsfile.release` is the pipeline job `release`, in the same Jenkins folder as the CI job and started by
hand. Its stages are Candidate, Checkout, Version check, Build and Publish (SPEC 12.5). Its parameters are
`VERSION` (`vMAJOR.MINOR.PATCH[-pre]`, required), `CI_BUILD` (a `ci/main` build number, empty for the last
successful one), `DRY_RUN` (default `true`), `REGISTRY` (no default), `IMAGE_NAME` (default `village-hall`) and the two
credential IDs, `REGISTRY_CREDENTIALS_ID` and `GITHUB_CREDENTIALS_ID`.

Candidate copies `ci-results/release-candidate.json` from the folder's `ci/main` job (the path is computed from
the job's own `JOB_NAME`; a build that was not fully green has no candidate, so the copy fails). Checkout checks
the candidate commit out and reads the repository from the remote. Version check refuses a malformed or taken
version. Build runs `ci/release-build.sh`, and Publish runs `ci/release-publish.sh` (below). With `DRY_RUN` true
everything is built and nothing is published. The job is never started by CI, and no file names a host.
`ci/repocheck/jenkinsfile_test.go` fails if the stages, the dry-run default, the derived paths, the parameters
or the single publish step change, or if a hostname appears in the file.

## Release build

`ci/release-build.sh <version> <outdir>` (`<version>` is `vMAJOR.MINOR.PATCH[-pre]`; the revision is
`BOARD_IMAGE_REVISION` or the checked-out commit) writes, at the top of `<outdir>`, the release's assets:
`vh_{linux,darwin}_{amd64,arm64}.tar.gz` (a `vh` at the archive root, built with `CGO_ENABLED=0`,
`-trimpath` and `-tags timetzdata`, the version and revision stamped in through `internal/version`,
name-sorted root-owned entries with the commit's own time), one CycloneDX SBOM per tarball
(`vh_<os>_<arch>.cdx.json`, Trivy `rootfs`), `village-hall-image.cdx.json` (Trivy `image` over the
amd64 image), `install.sh` and `INSTALL-AGENT.md` from the commit, and `checksums.txt`, `<sha256>  <file>`
for every other file at the top. The multi-arch image (linux/amd64 and linux/arm64, built by
`ci/image-build.sh` with `BOARD_IMAGE_NAME=village-hall:<version>` and `BOARD_IMAGE_TAG`, so
`board:ci` is untouched) is `<outdir>/image/board-image-oci.tar`: not an asset, it is what the publish
step pushes. When `BOARD_RELEASE_IMAGE_DIR` names a directory holding the `board-image-oci.tar` and
`board-image.tar` of an image `ci/image-build.sh` already built (the dry run sets it), `ci/release-build.sh`
builds no image: `ci/oci-name.sh` copies that OCI layout adding only the `village-hall:<version>` name to
its `index.json` (every blob and digest stays as built) and the image SBOM scans that `board-image.tar`.

`ci/release-check.sh [dir]` is the dry run, in the Image unit after `ci/image-check.sh`: it needs the
two image tars the unit's `ci/image-build.sh` left in `<dir>` (and stops, naming that command, when they
are missing) and builds `v0.0.0-dryrun` into `<dir>/release-dryrun` from them, with no second image build
(the image carries the unit's version, not `v0.0.0-dryrun`; the `vh` tarballs carry the dry-run version), runs `ci/repocheck`'s `TestReleaseBuild*` tests (build
tag `releasebuild`; they assert the four tarballs, that `checksums.txt` verifies and covers every
asset, that the linux/amd64 `vh version` prints the version, that every SBOM parses and that the OCI
index lists both platforms) through `ci/run.sh`, writes their JUnit to `<dir>/junit-merged/junit-release-dryrun.xml`,
keeps `<dir>/release-dryrun-checksums.txt` and removes the assets and the `village-hall:v0.0.0-dryrun` tag.

## Release publish

`ci/release-publish.sh <outdir>` publishes what `ci/release-build.sh` wrote. Its environment is `VERSION`,
`CI_BUILD`, `REGISTRY`, `IMAGE_NAME` (default `village-hall`) and `DRY_RUN` (default `true`); `CANDIDATE`
(default `candidate/ci-results/release-candidate.json`), `RELEASE_SHA`, `REPO`, `JENKINS_USER`/`JENKINS_TOKEN`,
`REGISTRY_USER`/`REGISTRY_PASSWORD` and gh's `GH_TOKEN` are optional. The seven steps (verify the candidate,
refuse an existing tag or release, create a draft, upload every asset, push the image and put its digest in
the notes, publish, read back `immutable: true`) are in SPEC 15.3. A dry run only reads. The tests are
`ci/repocheck/releasepublish_test.go`, against stub `gh`, `docker` and `curl`; nothing in CI reaches GitHub or a
registry.

## Quality gates

Every SPEC section 15.4 gate is in `ci/quality.json` with `enabled: true` at
its threshold. A gate whose input does not exist yet fails; it never passes.

| key | threshold | state |
|---|---|---|
| `coverage` | 85 total, 80 per package, 50 fixture | runs |
| `crap` | ceiling 8, fixture ceiling 8 | runs |
| `crap_ts` | ceiling 8, no baseline | runs (needs `web-coverage/` from `ci/suite.sh` and `web/node_modules`) |
| `frontend_coverage` | 85 total, 80 per file (statements and lines) | runs (needs `web-coverage/coverage-summary.json` from `ci/suite.sh`) |
| `golangci` | zero findings | runs |
| `govulncheck` | zero reachable | runs |
| `trivy` | HIGH, CRITICAL on the source | runs |
| `trivy_image` | HIGH, CRITICAL on the image | runs (`trivy image --input ci-results/board-image.tar`; fails when the tar, which `ci/image-build.sh` writes, is missing or empty) |
| `mutation_go` | efficacy 80 per package, `internal/domain`, `internal/store` and the Phase 2 application logic (see below), zero mutants fails | runs (fails until `ci/mutation.sh` has written `ci-results/mutation/`; with `--mutation-deferred` the Jenkins Mutation phase judges it) |
| `stryker` | break 80, per module 80, `web/src/lib` | runs (fails until `ci/mutation.sh` has written `ci-results/mutation/stryker/mutation.json`; with `--mutation-deferred` the Jenkins Mutation phase judges it) |
| `sbom_module` | CycloneDX of `go.mod`, non-empty | runs |
| `sbom_frontend` | CycloneDX of `web/package.json` and `web/package-lock.json` (dev dependencies included: all of the frontend's are), non-empty | runs |
| `sbom_image` | CycloneDX, non-empty | runs (from the image tar; fails when it is missing) |
| `openapi_lint`, `openapi_drift` | lint clean, no drift | runs (the same commands `ci/lint.sh` runs; fails when `api/openapi.yaml`, the generated code or `web/node_modules` is missing) |

`ci/quality.sh` exits non-zero when any row fails. Each row that fails for a
missing input names the input in the report.

After `ci/quality.sh`, the Quality unit runs `ci/run.sh go run ./ci/pkgtime -junit
ci-results/junit-go.xml -budgets ci/pkgtime/budgets.json`, and either failing fails
the unit. `ci/pkgtime` holds each package in `ci/pkgtime/budgets.json` to the test
time (`<testsuite time>`) the Suite unit's Go junit records for it: `internal/server`
at 28 s, at most 80% of the 35.855 s of main build 275. A package missing from the
junit fails too. A budget is only ever lowered. `ci/pkgtime/main_test.go` feeds it
junit fixtures with `internal/server` at the budget (pass) and one second over (fail).
To stay inside it, `internal/server`'s ask-wait tests run with sub-second windows
(`WithAskWaitUnit`, the test hook that shortens a second of `?wait=` in a test rig)
and its answer-cannot-be-stored cases run as parallel subtests.

## Pipeline tools and scripts

The Go tools under `ci/` have their own tests, and `ci/run.sh go test -count=1
./ci/...` runs them all:

- `ci/mutation.sh` and `ci/mutationgate`: the Go mutation gate, the command of
  the Jenkins Mutation unit (only with `MUTATION=true`, `MUTATION_FULL=true` or the
  nightly of `main`; a push build of `main` skips it). The
  script runs `gremlins unleash` over each package in `mutation_go.packages`
  (`internal/domain`, `internal/store`, and the application logic Phase 2
  introduced or changed: `internal/asks`, `internal/attention`,
  `internal/audit`, `internal/checkin`, `internal/cursors`, `internal/events`,
  `internal/feed`, `internal/idempotency`, `internal/notes`, `internal/tasks`, `internal/server`,
  `internal/cli`), writes one JSON report per package to
  `ci-results/mutation/` and then runs `mutationgate`, which reads the
  threshold from `ci/quality.json` and fails when
  - a package's efficacy (gremlins' definition, killed over killed plus lived)
    is below `mutation_go.efficacy` (80 %), or
  - a package generated no mutant, or judged none (all not covered or timed
    out): a gate that measured nothing has not passed (`fail_on_no_mutants`),
    or
  - a package's report is missing (the package never ran: its unit was cut or
    failed before gremlins finished it) or is not a gremlins report.

  `ci/mutation.sh` writes each package's report the moment that package
  finishes (to `<report>.json.part`, renamed whole), so a unit cut by its bound
  keeps every finished package's report (the unit's stash carries `ci-results`,
  and the cut-off archive lists `ci-results/mutation/*.json` and
  `ci-results/mutation-units/*/*.json`) and leaves no half-written file for the
  package it was running. The merge gate then judges each finished package on
  its efficacy and names only the unfinished ones as missing (`the package
  never ran`), not as unreadable.

  Diff mode (`BOARD_MUTATION_DIFF=<commit>`, read before the `BOARD_*` scrub;
  `ci/mutationgate -diff <commit>`) mutates only the lines changed since the
  merge base with that commit: units with no measured file changed are not run
  and pass, gremlins gets `--diff` (through a `git` shim that adds
  `--relative=<package dir>`, because gremlins 0.6.0 compares root-relative
  diff paths with package-relative file names and would skip every mutant),
  and a unit with no mutant or no judged mutant passes while one with mutants
  keeps the 80 % floor. Stryker runs only if `web/src/lib` changed.
  `BOARD_MUTATION_WORKERS` sets gremlins' workers and `BOARD_MUTATION_TEST_CPU`
  each worker's GOMAXPROCS (gremlins' own `--test-cpu` passes `-cpu N` as one
  argument, which fails every test run and counts every mutant killed).

  `internal/server`, `internal/cli` and `internal/tasks` hold older Phase 1
  code beside Phase 2's, and a gremlins run of a package re-runs that
  package's whole test suite for every mutant, so each is narrowed to the
  files Phase 2 introduced or changed, listed under `mutation_go.scopes` in
  `ci/quality.json` (a listed file is mutated whole, its older lines
  included). `mutation_go.phase_base` is the commit Phase 2 started from:
  `ci/mutationgate -unmeasured` runs `git diff` against it and prints every
  non-test `.go` file under `internal/` or `cmd/` changed since then that no
  unit measures (generated files, marked `Code generated ... DO NOT EDIT.`,
  are left out), exiting 1 if there is one, and a test of the shipped config
  requires none, so a Phase 2 file left out of the scope fails the Suite.
  `ci/mutationgate -excludes <pkg>`
  prints one `--exclude-files` regexp per other non-test file, and
  `ci/mutation.sh` passes them to gremlins (a scoped file that does not exist
  fails). A gremlins run of `internal/server` re-runs a large suite for every
  mutant, so `mutation_go.parts` splits its scope into three disjoint
  parts (`ops`, `streams` and `core`), and `internal/cli`'s into two (`views`
  and `core`), each set together exactly its scope, which `ci/mutationgate`
  checks: each part is a unit `internal/server#ops` that gremlins runs with
  every file outside the part excluded, with its own report
  (`internal-server-ops.json`) and its own judgement against the same floor.
  `mutation_go.lanes` partitions the packages' units into lanes (a unit in no
  lane, or two, fails `ci/mutationgate`). Jenkins' full mode runs three units,
  each over a comma-separated list of lanes; a list may also name single units
  (`internal/server#core`), which is how incremental mode runs only the stale
  ones (`ci/mutationgate -stale <ref>` lists them, exit 3 when every unit must
  run). A lane is also the way to run part of it by hand:
  `ci/mutation.sh [outdir [config [lane]]]` runs one lane (default `all`; Stryker
  also runs in `base`) and `ci/mutationgate -lane` judges only that lane's
  reports; every lane writes into `ci-results/mutation/`, where `ci/quality.sh`
  judges them all. gremlins takes a
  package's per-mutant bound from the time of its covered `go test` run, which
  go's test cache can answer in milliseconds, so `ci/mutation.sh` runs with
  `GOFLAGS=-count=1`: the covered run is a real run, and `--timeout-coefficient
  60` multiplies the suite's real duration (without it every `internal/server`
  and `internal/cli` mutant timed out unjudged). The efficacy floor, `--timeout-coefficient 60` and the worker count
  (`nproc / 3` workers at GOMAXPROCS 2 per full-mode unit and `nproc / 2` in diff mode in Jenkins) apply to every package alike; nothing is raised for the
  scoped ones. `ci/qualitycheck` reports a scope narrowed against the base as a
  loosening, like a dropped package.
  Counts come from the per-mutation statuses, since gremlins' summary fields
  omit timed-out and not-covered mutants. The script passes
  `--timeout-coefficient 60`: with a warm build cache gremlins' default bound
  timed out every `internal/store` mutant. `ci/quality.sh` evaluates the same
  reports as its `mutation-go` gate and fails when `ci-results/mutation/` is
  absent. Run it with `ci/run.sh ci/mutation.sh`, which is also the Jenkinsfile
  Mutation unit's command (see "The Jenkinsfile").
- The frontend gates (`ci/qualitycheck/frontend.go`, tests in `frontend_test.go`
  and in `web/scripts/*/*.test.ts`, which Vitest runs):
  - `web/scripts/crap/` scores every function of `web/src` (`*.ts`, tests
    excluded) as ESLint complexity against the Vitest v8 coverage in
    `coverage-final.json` and fails above `crap_ts.ceiling` (8), which the
    script reads from `ci/quality.json`. There is no baseline and no exemption
    list. Logic that a Svelte component needs belongs in a `src/lib` module,
    where it is scored, covered and mutated. Run it by hand with
    `ci/run.sh sh -c 'cd web && npm run test && npm run crap'`.
  - `frontend_coverage`: `coverage-summary.json` must show statements and lines
    at or above `total_floor` (85) over the frontend and `file_floor` (80) for
    every file; a summary that lists no file fails. Vitest measures
    `src/lib/**/*.ts` only (`web/vitest.config.ts`).
  - `stryker`: `ci/mutation.sh` also runs `npm run mutation` in `web/`
    (`web/stryker.config.mjs`: `src/lib`, `thresholds.break` 80). Stryker's own
    break judges the whole run; `web/scripts/mutation/` also fails any one module
    below `stryker.module_break` (80) and checks that the config's break and path
    equal `ci/quality.json`'s. The report is copied to
    `ci-results/mutation/stryker/mutation.json`, which `ci/quality.sh` judges.
    `web/scripts/mutation/vitestFileRunner.mjs` (from s3-browser) narrows a mutant
    run to the covering test files because Vitest 5 matches `-t` differently.
- `ci/covgate`: coverage floors from `ci/quality.json` (total, per package,
  fixture) over the merged profile. A package with no statements is named by
  exact path in `zeroStatementPackages`; a stale or missing entry fails.
- `ci/crapgate`: CRAP score (cyclomatic complexity against coverage) per
  function against the ceiling in `ci/quality.json`. There is no baseline file.
- `ci/legcompare`: compares per-leg JUnit and lists the scenarios that are
  green on one database leg and red on another (exit 1).
- `ci/pkgtime`: fails when a package's test time in `ci-results/junit-go.xml` is over
  its budget in `ci/pkgtime/budgets.json` (Quality runs it after `ci/quality.sh`).
- `ci/qualitycheck`: the single quality-gate runner behind `ci/quality.sh`. It
  runs whichever gates `ci/quality.json` switches on (every row of the table
  above) and fails if a threshold is loosened against the
  merge-base copy of the file. A table-driven test (`completeness_test.go`)
  holds the real file to SPEC section 15.4 and proves that removing,
  disabling or loosening any gate is noticed.
- `ci/rerun-decision.sh`, `ci/flakygate.sh` (tested by `ci/repocheck/rerun_test.go`):
  the infrastructure-only rerun decision and the build failure on a non-empty
  flaky or leg-divergence list.
- `ci/attempt-id.sh` (tested by `ci/repocheck/attemptid_test.go`): the cleanup id
  of a unit attempt, unique per job, build, unit and attempt.
- `ci/loadsample.sh` (tested by `ci/repocheck/loadsample_test.go`): the per-minute
  load average, CPU busy and iowait samples of the E2E unit's run.
- `ci/junit2allure`, `ci/junitflaky`, `ci/allureverify`: JUnit to Allure
  conversion, the flaky-test list, and the check that the Allure report keeps
  the Godog steps and issue links.
- `ci/suitecheck`: probes of `ci/suite.sh` against small fixture modules (the
  `BOARD_*` scrub, merged coverage, no retries, timeouts, Allure results).
- `ci/lintcheck`: probes of `ci/lint.sh` (stubbed tools: every stage runs in
  order and a failing stage stops the rest) and of the golangci-lint config, plus
  one fixture per added checker (`fixtures_test.go`): ESLint, svelte-check, tsc,
  shellcheck, the OpenAPI lint and the generated-code drift check each pass on an
  unmodified copy of the tree and fail on one planted violation. They run the
  real tools, so they need `web/node_modules` (the test runs `npm ci` itself on a
  fresh checkout).
- `ci/repocheck`: repository probes. Besides the Dockerfile digest and
  `ci/run.sh` argv checks above, it asserts that go.mod carries a
  `toolchain go1.N.P` line naming a patched release no older than the `go`
  directive (`toolchain_test.go`), and that golangci-lint is one shared
  invocation switched on in `ci/quality.json`, while gofmt, `go vet` and
  `go mod tidy` run only in `ci/lint.sh` and in no other CI script or pipeline
  file (`lintgate_test.go`).

Scripts, each run through `ci/run.sh` unless noted:

- `ci/suite.sh`: `BOARD_CI_PLAYWRIGHT` selects the parts that run (`with`, the
  default: everything; `skip`: all but Playwright, the Jenkins Suite unit; `only`:
  the web build and Playwright, the Jenkins E2E unit; anything else is refused);
  `ci/suitecheck/playwrightmode_test.go` runs the real script in each mode.
  `go test -p=1 -count=1 ./...` through gotestsum in two passes
  (every package but `features/TestFeatures`, then `TestFeatures`), with
  no retries: Vitest, Playwright, the Go package matrix and `features/TestFeatures`
  (once per database leg) each run once, with no repeat, no `gotestsum
  --rerun-fails` and no per-scenario Godog rerun, and a failure in any of the four
  makes the script exit non-zero. It writes no flaky list. A diagnostic rerun of
  failed tests, if one is ever added, must write under the results directory and
  never change the exit status. `ci/suitecheck/noretry_test.go` pins each of the
  four points by running the real script against a fixture whose test fails on its
  first run, and fails against the version of the script before they were removed
  (`SUITECHECK_SUITE_SH` points the package at another copy of the script). It
  unsets every `BOARD_*`
  variable after reading its own `BOARD_CI_*` settings. It empties the unit and
  feature coverage-data directories first, so a run into a results directory
  used before never merges stale counters. `BOARD_CI_OUT` or a
  positional argument names the results directory.
  The feature passes get `GOCOVERDIR` and `BOARD_FEATURES_COVERMODE` (the unit
  pass's mode, `atomic` under `-race`): the Godog harness then builds the real
  `vh` binary with `-cover -covermode=<mode>` (`features/harness_test.go`), and
  stops a scenario's server with SIGTERM so its counters are flushed. The suite
  merges those counters with the unit pass's into `coverage-merged.out`. A
  database leg whose log shows scenarios ran but that left no non-empty counter
  file in the feature coverage directory fails the suite and is named, as does a
  run whose feature profile has no blocks; a unit-only merged profile is never
  accepted. `features/coverage_test.go` pins the build line, the emitted
  counters and the server flush; `ci/suitecheck` probes the suite's refusal per leg.
  When `web/package.json` exists it first runs `npm ci` and `npm run build` in
  `web/` (the build lands in `internal/webui/dist`, so the Go passes embed the
  real UI rather than the placeholder page), then `npm run test` (Vitest with
  `@vitest/coverage-v8`), writing `junit-vitest.xml`, `web-coverage/` and
  `vitest.log` into the results directory. Vitest runs once, and a failure fails
  the suite. `ci/suitecheck/vitest_test.go` pins the files and `noretry_test.go`
  the single run.
  It then starts Playwright (`e2e/`, after `npm ci` there) in the background, beside
  the Go passes and the Godog passes, which share nothing with it (each Playwright worker has a free
  port and a SQLite store of its own); the script waits for it before it merges the
  JUnit files, replays its messages to stderr and fails the suite on its failure. On
  Jenkins that is not how it runs: the Suite unit sets `BOARD_CI_PLAYWRIGHT=skip` and
  the E2E unit `BOARD_CI_PLAYWRIGHT=only` (the step in the foreground, after the web
  build, and nothing else), so the two run in parallel. Nothing is shortened: the same
  specs run with the same bounds. The config never retries
  (`retries: 0`) and starts no server: it has no `webServer`, `globalSetup` or
  `globalTeardown`. Only `GET /healthz` is open.
  Each Playwright worker starts a board server of its own (`e2e/worker-server.ts`
  runs `e2e/serve.sh` in a directory of the worker's own) before its first test:
  its own port, work directory (binary and SQLite store) and API key, minted with
  `vh admin keys create` at test time (no key is committed), stopped after its
  last test. These are worker-scoped fixtures of the `test` exported by
  `e2e/fixtures.ts`: a spec reaches its worker's server through `baseURL`, and its
  key and store through `process.env.E2E_API_KEY` and `E2E_WORK`, which the fixture
  sets in that worker's own process; `e2e/store.ts` therefore writes to the calling
  worker's store. Every `e2e/*.spec.ts` imports `test` from `fixtures.ts`, never from
  `@playwright/test` (`TestE2ESpecsTakeTestFromFixtures` in `ci/suitecheck` fails
  otherwise; `SUITECHECK_E2E_DIR` points it at another e2e directory).
  `keyedTest` (smoke, swagger) also sends the key and a `Board-Actor` header on every
  request. `e2e/worker-isolation.spec.ts` seeds a task through each of two workers
  and asks the other worker's server for it; the project `worker-isolation` runs
  it alone, first, so that two workers are alive together. Playwright never shares a
  worker between projects, so each project's workers have servers and stores of
  their own, and that groups the rest: the `lists` project holds `a11y.spec.ts`,
  `dialogs.spec.ts` (it Tabs to a button below the Initiatives list, 150 presses at
  most), `shell.spec.ts`, the `timeline*.spec.ts` specs and `wide.spec.ts`, whose
  cost follows the size of their store (an unfiltered Timeline or Initiatives list,
  a Tab walk past it), so what the other specs add never lengthens it; `chromium`
  holds every other spec. It is store isolation, not an order: no project waits for
  another except on `worker-isolation`, and the specs run in any order.
  `a11y.spec.ts` seeds its board once per worker (a `beforeAll`), loads one page per
  test, runs axe's full rule set in the light pass and colour contrast only
  (`color-contrast` and `link-in-text-block`) in the dark pass (R9 g);
  `TestA11ySpecRunsFullSetLightAndColourRulesDark` in `ci/suitecheck` pins it.
  `sameorigin.spec.ts` seeds in a `beforeAll`, runs its per-view loop at the desktop
  viewport only and waits for explicit readiness (a ready locator, then the recorded
  request list reading the same on two consecutive polls, `e2e/settle.ts`); no `e2e/*.ts` file
  waits for `networkidle` (`TestNoE2EFileWaitsForNetworkIdle` in `ci/suitecheck`, R9 h). `e2e/run-dir-reporter.ts` removes the run's directory
  of worker directories when the run ends.
  Logic and layout viewports (R9 i): `e2e/viewports.ts` exports `logicViewports` (desktop
  only) and `layoutViewports` (phone and desktop). A spec with no viewport-dependent
  assertion (no branch on the viewport, no phone-only locator, no tap, no width, scroll or
  position measurement, no axe scan) takes its viewports from `logicViewports` and so runs
  at the desktop width only; a spec that checks layout or touch behaviour keeps its phone
  variant (shell, dialogs, touch, density, overflow, timeline-phone, wide, a11y, keys,
  initiatives, history_restore, task_page, tasks-view, needs-me, timeline, timeline-active
  and timeline-rows among them). `wide.spec.ts` measures the R13 caps at 2560 and 3440 px
  and takes the shared `phone` from `e2e/viewports.ts` for its phone variant, which checks
  that below the cap each view and the Timeline frame take the whole main column and a long
  description stays within 75ch and the column. Every phone-dropped spec still runs each
  test it had at desktop. `TestPhoneDroppedSpecsUseLogicViewports` in `ci/suitecheck`
  (`viewports_test.go`) holds the explicit list of phone-dropped specs: each imports
  `logicViewports`, declares no viewport list of its own and contains none of those
  patterns; any other file that uses `logicViewports` fails it. Each spec that must keep its
  phone variant must also run at the phone viewport: it imports `phone` or
  `layoutViewports` from `e2e/viewports.ts`, or declares a 390 px wide viewport
  (`TestKeptSpecsRunAtThePhone`).
  Playwright runs once and
  the script does not repeat a failed run: a failure fails the suite.
  `junit-playwright.xml` and the HTML, Allure and JSON output (`playwright/`)
  land in the results directory, and `ci/summary.sh` counts the JUnit files.
  `ci/suitecheck/playwright_test.go` pins this: it fails if `retries` is not 0, if
  the config brings back a `webServer`, `globalSetup`, `globalTeardown` or
  `sh serve.sh`, or if the worker-scoped server fixture is dropped.
  `e2e/serve.sh`, which `e2e/worker-server.ts` runs once per worker, also builds and starts `cmd/fake-oidc` (the mock OIDC provider,
  test-only, signing in `operator@example.test`) and starts the binary with browser
  login against it (`BOARD_OIDC_*`, `BOARD_PUBLIC_URL`; the client secret is made
  up per run). `e2e/login.spec.ts` uses `test`, not `keyedTest`: it sends no key,
  the session cookie is its only credential.
  `serve.sh` builds the UI only when `internal/webui/dist` is missing, so after
  changing `web/` locally run `npm run build` there first (the suite always does).
  One spec: `ci/run.sh sh -c 'cd e2e && npx playwright test smoke.spec.ts'`.
- `ci/quality.sh`: scrubs `BOARD_*` and runs `ci/qualitycheck` over
  `ci/quality.json` and the suite's merged coverage profile.
  **CRAP ceiling 8**, for product code and for fixtures alike; coverage floors
  are 85 % total, 80 % per package and 50 % per fixture. No gate may be
  switched off or loosened: `ci/qualitycheck` fails on a change that does.
  There is no baseline or exemption file. The CycloneDX files the SBOM gates
  write go into the results directory.
- `ci/lint.sh`: fail-fast, in this order: gofmt, `go vet`, `go mod tidy` drift,
  `ci/golangci.sh` (the golangci-lint invocation `ci/quality.sh` shares,
  configured by `.golangci.yml`; a `//nolint` must name its linter and give a
  reason), shellcheck over `git ls-files '*.sh'` (no ignore list), then, from
  `web/` (`npm ci` runs first when `web/node_modules` is missing), ESLint
  (`web/eslint.config.mjs`, `--max-warnings 0`), `svelte-check
  --fail-on-warnings`, `tsc --noEmit`, the Redocly lint of `api/openapi.yaml`
  (`web/redocly.yaml`: `recommended-strict`, every finding is an error, plus
  `operationId`, summary, description, licence and tag-description rules; the
  one rule left off is `operation-4xx-response`, because `GET /healthz` has no
  failing client path) and `ci/openapi-drift.sh`. Last comes `go run ./ci/installcheck` (R9): it parses `install.sh` and
  `INSTALL-AGENT.md` (the shared-facts list at the top of the guide) and fails when they disagree on the
  asset name pattern, the OS list, the arch list, the checksum file name, the default install directory
  or the set of `VH_*` variable names; its fixture pairs live in `ci/installcheck/testdata`.
- `ci/openapi-gen.sh` writes the generated code from `api/openapi.yaml`:
  `internal/server/openapi.gen.go` (oapi-codegen in `std-http,strict-server,models`
  mode, pinned by go.mod's `tool` directive, so no image change) and `web/src/lib/api/schema.d.ts`
  (openapi-typescript, pinned by `web/package-lock.json`; its `typescript` peer
  range is overridden in `web/package.json` because it predates TypeScript 6).
  `.golangci.yml` lints generated Go like any other file, so the script
  rewrites the two kinds of line oapi-codegen emits that errcheck,
  staticcheck and gosec reject (an unchecked `Close`, a
  `Write([]byte(fmt.Sprint(...)))`) and fails when the generator stops emitting
  one, so a generator upgrade cannot leave a stale rewrite behind. It makes a
  third rewrite for the CRAP gate (ceiling 8, generated code scored like any
  other): the six-line block oapi-codegen repeats for every query parameter
  that cannot be bound becomes one call of `bindFailed` in
  `internal/server/params.go` (the same two answers), and the now unused
  `errors` import is dropped. A fourth rewrite folds each run of those calls
  into `bind.check(name, runtime.Bind...)` on a `queryBinder`
  (`internal/server/params.go`) that answers only the first failure, closed by
  one `if bind.failed { return }`, so an operation with nine filters
  (`listEvents`) has the complexity of one branch and stays under the ceiling.
  `ci/openapi-drift.sh` regenerates both into a scratch directory and fails when
  either checked-in file differs or is missing. Run
  `ci/run.sh ci/openapi-gen.sh` after every edit of the document and commit the
  result. `web/src/lib/api` is excluded from Vitest coverage and Stryker: it is
  generated type declarations with no statements.
- `ci/image-build.sh [tar]`: builds the root `Dockerfile` for linux/amd64 and
  linux/arm64 (cross-compiled, no emulation, nothing pushed), loads amd64 as
  `board:ci` and writes `ci-results/board-image.tar` and
  `ci-results/board-image-oci.tar` (the Jenkins Image unit; on the agent). It passes the
  image's `VERSION` and `REVISION` build args (`BOARD_IMAGE_VERSION`, `BOARD_IMAGE_REVISION`, set by
  the Jenkinsfile to the commit's tag or short sha and the full commit), which the binary reports in
  `vh version` and `GET /api/v1/meta`.
- `ci/image-check.sh [dir]`: runs `ci/imagecheck` over the OCI layout, `vh version`,
  the deployed-configuration test and a `/healthz` probe against `board:ci`.
- `ci/release-build.sh <version> <outdir>`: builds the release assets of SPEC 12.5 into
  `<outdir>` (inside the checkout; on the agent, outside `ci/run.sh`) and pushes, uploads and tags
  nothing; see "Release build" below. `ci/release-check.sh [dir]` is its dry run: it reuses the Image
  unit's image tars (`BOARD_RELEASE_IMAGE_DIR`, `ci/oci-name.sh`) and builds no image. `ci/imagebuildonce_test.go`
  (`ci/repocheck`) runs the scripts against stub `docker`, `go`, `trivy` and `ci/run.sh` and counts the
  `ci/image-build.sh` calls: none in the dry run, one in a release.
- `ci/imagecheck`: the Go tool that reads the OCI layout (both manifests, user,
  entrypoint, title, compressed size, the ELF machine of `/vh`); its `imagedeploy`
  test (`deployed_test.go`) starts the loaded image as the deployment does.
- `ci/stability.sh`: the suite N times, each run labelled by the exit status of
  `go test` itself.
- On the Jenkins agent, outside `ci/run.sh`: `ci/jenkins-image.sh` (builds
  `board-ci:local` and fixes the cache volume's owner), `ci/allure-report.sh`
  (pinned, checksum-verified Allure CLI), `ci/allure-smoke.sh`,
  `ci/jenkins-report.sh` and `ci/summary.sh` (the Allure report and CI summary
  pages).

## Known differences from deck

The `ci/` tools and scripts are carried from deck (`DECK_*` renamed to
`BOARD_*`, deck's module, image, volume and label names renamed to this repository's).
Every test in deck's copies of `ci/covgate`, `ci/crapgate`, `ci/qualitycheck`,
`ci/junit2allure`, `ci/junitflaky`, `ci/allureverify`, `ci/suitecheck`,
`ci/lintcheck` and `ci/repocheck` is carried, adapted to Village Hall's tree (CRAP
ceiling 8 instead of 10, the zero-statement list naming `features/`), except:

- **tmux-only tests.** Village Hall has no TUI: `ci/tmuxguard/`, `ci/tmux-guard.sh`
  and the `tmux.version` assertion in deck's `ci/suitecheck/allure_test.go`
  are not carried.
- **Install tests, rewritten for Village Hall.** deck's `ci/repocheck/installcheck_test.go`
  and `TestInstallScriptCurlIsHTTPSOnlyAndStatesItsChecksumLimit` are not carried as
  they were: `ci/repocheck/install_test.go` runs `sh install.sh` against an httptest
  release server with a fake `uname`, covers SPEC §8.5's immutability check (not
  deck's checksum-only one) and fails any `curl` call without `--proto =https`.
  They run in the Suite's Go pass of the Core phase, like every `ci/repocheck` test.
  `vh update` is tested the same way: `internal/update` and `features/cli_update.feature` point
  `VH_API_URL` and `VH_DOWNLOAD_URL` at a loopback stub release server. The installer and the
  updater download anonymously over HTTPS (the repository is public); no test and no CI step calls
  the real GitHub release API.
- **Jenkinsfile tests, carried here.** deck's `ci/repocheck/jenkinsfile_test.go`
  and its `TestLintJobRunsGolangciLintBeforeTheSuite` (deck's
  `ci/repocheck/lintgate_test.go`, here `TestLintUnitRunsGolangciLintBeforeLaterPhases`)
  are carried against Village Hall's `Jenkinsfile`, renamed (`DECK_*` to `BOARD_*`, the
  `deck/` jobs to `board/`) and rewritten for its three phases (Core, Stability,
  Mutation) and parallel units. Village Hall's pipeline adds Mutation and the image
  build, and drops the tmux-only parts. deck's release
  assertion, `TestJenkinsfileReleasePublishesTheCandidate`, is carried for Village Hall's
  `Jenkinsfile.release` under the same name: it asserts the five stages in order, the
  `DRY_RUN` default, the CI job path derived from the folder of `JOB_NAME`, the repository read
  from the checkout's remote, the `REGISTRY`, `IMAGE_NAME` and two credential-ID parameters, no hostname
  anywhere in the file, and that Publish's only shell step is `ci/release-publish.sh`, with planted violations
  that each must be reported.
