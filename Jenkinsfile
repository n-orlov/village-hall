// board CI on the homelab Jenkins: multibranch job board/ci (see docs/ci.md).
//
// Distributed pipeline: every test stage is a "unit" that runs as its own
// parallel branch with its own retry policy and timing, instead of one stage
// after another. Every unit runs on int21h-ws
// (amd64, 12 hardware threads, 4 executors): the arm64 phones of the board-test pool
// were too slow (#16 took 5 h, mutation lanes ran out of 105 minutes and
// agents dropped under load 16-53), so no unit asks for them any more. Units
// that find all 4 executors busy queue for one.
//
// Retry policy, per unit (on the same node: there is only one):
//   - infrastructure failure (agent lost, Docker unavailable, checkout or CI
//     image build failed, the attempt went silent, no executor free for the
//     whole wait): another attempt, up to 3 infrastructure failures;
//   - the unit's command fails (an attempt outcome fail:<exit status>): the
//     unit fails and is not rerun, and nothing marks a build UNSTABLE for a
//     flake: a rerun that passed would only hide a test that cannot be
//     trusted. ci/rerun-decision.sh makes the call: only infra:* outcomes
//     rerun. A non-empty flaky list (ci-results/flaky-*.txt) or leg-divergence
//     list (ci-results/leg-divergence.txt) fails the build in the report
//     (ci/flakygate.sh);
//   - the unit runs out its own time bound on a node (its minutes below): a
//     failure, not an infrastructure failure, and never retried (operator
//     ruling 001-WAVE: three 45-minute attempts of a Suite that cannot fit
//     cost a push build 2 h 15 min). Only a wait with no node stays infra.
// A unit may cap its attempts (maxAttempts: each full mutation unit has one).
// Each attempt reports node, queue wait and run time; the build page gets a
// per-unit timing table (ci-results/distributed/timing.md).
//
// Each unit checks out the build's commit in its own workspace, builds
// board-ci:local (ci/jenkins-image.sh; cached by the Docker daemon), runs,
// removes the sibling containers it labelled (ralphd.run=<attempt id>, by
// exact ID; the id names the job, so another branch's build with the same
// number never matches) and stashes its ci-results. Quality waits for Suite (coverage),
// E2E (Playwright's results) and Image (the image tar). The report runs once
// at the end over every unit's stash. An attempt cut off before its stash (its
// own limit, a lost agent) writes ci-results/unit-cut-<unit>.txt, stashes and
// archives the logs, JUnit files and finished mutation reports written so far,
// so Quality reads them (ci/mutation.sh writes each package's report as soon as
// the package finishes, so a cut mutation unit keeps every finished package's;
// the gate names only the unfinished ones as missing: "the package never ran").
//
// Unit runs: the Suite and E2E units and the three full mutation units run
// their command through unitSh, which runs it under ci/unitrun.sh with a bound
// a little inside the attempt's own limit (the limit less the time checkout
// and the image build used and 2 minutes). A command that outlives it is
// killed by the script (exit 124, an attempt outcome fail:timeout, never
// retried) instead of by the Jenkins timeout; one that fails or is cut keeps
// every junit-*.xml, log, flaky list and finished report in ci-results, and
// the script's failure summary (how the unit ended, then each failing test
// read from the junit files: spec file and title, or package and test) goes
// to the console and the build description and to
// ci-results/unit-failure-<unit>.txt in the unit's stash.
//
// Phases; each phase's units run in parallel:
//   1. Core (every build): Lint, Suite, E2E, Image, Allure smoke, then
//      Quality (4 executors: Quality waits for Suite, E2E and Image without
//      holding one). Suite is ci/suite.sh with BOARD_CI_PLAYWRIGHT=skip (45
//      minutes, 120 EXTENDED) and E2E is the same script with
//      BOARD_CI_PLAYWRIGHT=only: the Playwright suite, one worker, run once
//      (the script repeats no failed run, of Playwright or anything else;
//      120 minutes), in parallel with Suite, which no longer waits for it.
//   2. Stability and Mutation, side by side, once Core passed:
//      - Stability (EXTENDED=true, or nightly on main): one unit (one
//        executor) running the three stability runs side by side, each in its
//        own clone of the checkout and its own stage (about 11 minutes; until
//        #238 one unit each, which kept mutation units waiting for an
//        executor); EXTENDED also adds -race and fresh vulnerability
//        databases to Core.
//      - Mutation (MUTATION, MUTATION_FULL, or the nightly of main: the
//        timer, or EXTENDED=true on main), in one of three modes:
//        - full (MUTATION_FULL=true, MUTATION_MODE=full, Sunday's nightly,
//          or a nightly that cannot be incremental): three units in
//          parallel (FULL_MUTATION below), each ci/mutation.sh over its own
//          lanes of ci/quality.json into its own outdir, every lane in
//          exactly one unit and Stryker in the unit with lane base; then the
//          Mutation gate unit merges their reports and judges every package
//          once (ci/mutation-merge.sh, lane all). The three units start
//          together, beside the Stability unit: 1 + 3 units fit the 4
//          executors. Each gremlins runs nproc/3 workers at GOMAXPROCS 2
//          (nproc/2 when at most two units run), 12 workers in all on
//          int21h-ws, beside the stability runs for their first 21 minutes
//          (nice 10 leaves them ahead); Stryker, at the end of its unit, runs
//          nproc/2 test processes. A worker is busy about half the time
//          (compiling, waiting for its Postgres testcontainers), so 12
//          workers keep about nproc threads busy: #242 ran the CPU 81-99%
//          busy at load 20-30 and its units took 72-85 minutes. #244 halved
//          the workers to nproc/6: the CPU fell to 30-45% busy, and the
//          streams+cli unit (internal/server#streams, internal/cli#core and
//          #views, about 1.4 x slower each) hit its 110-minute limit. Each
//          unit has one attempt under 110 minutes and a hard memory cap
//          (MUTATION_MEMORY). ci/mutation.sh caps a mutant at about 5
//          minutes (its header), which a hanging internal/server mutant
//          used to hold for an hour: the one full unit of #233 took 3 h 56
//          min. #235 (four units, three of them
//          waiting for the stability units, Stryker on 11 processes) ended
//          mutation 80 minutes after Core, at 128 minutes in all.
//        - incremental (the nightly's default, MUTATION_MODE=incremental):
//          the reference is the last successful build of this job whose
//          description says mutation(full). The Mutation plan unit copies
//          its mutation reports and commit and runs ci/mutation-plan.sh: a
//          unit no change since that commit can reach (ci/mutationgate
//          -stale) reuses its report, the rest run in the full-mode units
//          (a unit with nothing stale is skipped), Stryker runs only when
//          web/ changed, and the gate merges the units' reports with the
//          reused ones, so every package is still judged exactly once. Full
//          instead on Sunday's nightly, with no reference, or when a shared
//          input changed (go.mod, go.sum, the CI image, the scripts that run
//          gremlins, mutation_go's packages, scopes or parts). The build's
//          description says mutation(incremental: N run, M reused from #K).
//        - diff (MUTATION=true on any other build): one unit, only the lines
//          changed since a base, under 45 minutes (minutes when few packages
//          changed; a changed internal/server still pays its covered suite
//          run once). A branch or PR build diffs against the merge base with
//          its target (origin/main, or CHANGE_TARGET), fetched into the
//          workspace first because the multibranch checkout fetches the
//          built branch only. A build of main itself would get HEAD as that
//          merge base, so it diffs against HEAD~1 (the last commit; the
//          nightly full run covers the rest). ci/mutation.sh and
//          ci/mutationgate -diff: a package with no mutant passes, one with
//          mutants keeps the 80 efficacy floor; Stryker runs only if
//          web/src/lib changed.
//   3. Perf (with Stability): the SPEC 14 harness, 30 minutes, after phase 2,
//      alone on the agent, when the Stability unit passed (a red
//      mutation gate does not cost the build its perf numbers).
// A phase runs only if every earlier one passed (UNSTABLE counts as passed).
// Nightly with full mutation: about 130 minutes (Core about 33); incremental
// with internal/server unchanged: about an hour.
@Library('homelab') _
import groovy.transform.Field

// Units' results and attempts, shared by the parallel branches.
@Field def results = [:]        // unit -> 'pass' | 'fail'
@Field def attemptLog = [:]     // unit -> [attempt maps]
@Field def signals = [:]        // 'Suite' / 'E2E' / 'Image' -> true once the unit ended (pass or fail)
@Field def stashedUnits = [:]   // unit -> true once an attempt stashed its results
@Field String commitSha = ''
@Field String imageVersion = ''
@Field String mutationBase = ''  // diff mode: the commit the mutation diff starts from
@Field String mutationPlanText = ''  // incremental mode: ci/mutation-plan.sh's plan file
@Field final String AMD = 'board-test && amd64 && docker'
// Full mutation's units (see the header): every lane of ci/quality.json's
// mutation_go.lanes in exactly one unit (ci/repocheck checks the union), and
// lane base carries Stryker, which ci/mutation.sh runs after the unit's
// gremlins. Balanced on #235's times (four units of 4 workers; minutes per
// lane, read off its unit times and #233's split): server-core about 50,
// server-ops 45, logic 43, cli 37, server-streams 32, cli-core 27, base 5,
// Stryker 43, so about 93 + 96 + 93 there. Each internal/server part sits in
// a unit of its own, so an incremental run with internal/server changed
// runs its three parts side by side. All three start together beside the
// one Stability unit (4 executors); dir is its outdir under
// ci-results/mutation-units/.
// Every mutation unit's hard memory cap (ci/run.sh's BOARD_CI_MEMORY, see the
// header): its container's cgroup, swap included, so a mutant that runs away
// is OOM-killed alone (go test exits 1: gremlins counts it KILLED) instead of
// the host's global OOM killer picking anything on int21h-ws. A mutated
// cli.test of #242 reached 54 GB (about 20 global OOMs since 10 Nov, every
// full cli run). A normal unit peaks near 5.5 GB with 2 workers, nearly all
// of it page cache of gremlins' work copies (anonymous memory under 0.5 GB,
// and the cgroup reclaims page cache before it kills); 3 x 12 GB beside
// Stability and the Postgres testcontainers (outside the cap) leave the 64 GB
// host headroom. A probe of internal/cli under 12g reproduced the runaway:
// the cgroup OOM killer took that cli.test alone within 15 s
// (CONSTRAINT_MEMCG) and gremlins went on, every mutant judged; in #244 a
// runaway cli.test (12.3 GB anonymous) was killed the same way, with no
// global OOM on the host.
@Field final String MUTATION_MEMORY = 'BOARD_CI_MEMORY=12g'
@Field final List FULL_MUTATION = [
  [name: 'Mutation server-core+logic', dir: 'server-core', lanes: 'server-core,logic'],
  [name: 'Mutation streams+cli', dir: 'cli', lanes: 'server-streams,cli,cli-core'],
  [name: 'Mutation ops+base+Stryker', dir: 'base', lanes: 'server-ops,base'],
]

long nowMs() { System.currentTimeMillis() }

@NonCPS
String slug(String s) { s.toLowerCase().replaceAll('[^a-z0-9]+', '-') }

// What ended an attempt that threw. Returns null for an abort of the whole
// build, which must propagate; otherwise an 'infra:<why>' outcome, or
// 'fail:timeout' for a unit that ran out its own time bound on a node.
String classify(Throwable e, Map a) {
  if (e instanceof org.jenkinsci.plugins.workflow.steps.FlowInterruptedException) {
    // getCauses needs a script approval; without it, treat as an abort.
    String causes = ''
    try {
      causes = e.causes.collect { it.toString() }.join(' ')
    } catch (Throwable ignored) {
      causes = 'unknown'
    }
    if (causes.contains('ExceededTimeout') || causes.contains('Timeout')) {
      // On a node, the unit ran out its own bound: a failure that is not
      // retried. No node yet is only a queue wait (infrastructure).
      return a.node ? 'fail:timeout' : 'infra:no-node'
    }
    if (causes.contains('RemovedNode') || causes.contains('Offline') || causes.contains('QueueTaskCancelled')) {
      return 'infra:agent-lost'
    }
    if (causes.contains('UserInterruption') || causes == 'unknown') {
      return null
    }
    return 'infra:interrupted'
  }
  // A pipeline-code error (serialization, sandbox) fails the same on any
  // node: never worth another node.
  if (e instanceof java.io.NotSerializableException ||
      e.toString().contains('RejectedAccessException') ||
      e instanceof groovy.lang.MissingPropertyException ||
      e instanceof groovy.lang.MissingMethodException) {
    return null
  }
  return 'infra:' + e.toString().take(80)
}

// Runs a unit's command through ci/unitrun.sh (see the header): under a bound
// a little inside the attempt's own limit, so a command that outlives it is
// killed by the script, which then writes the failure summary, instead of by
// the Jenkins timeout with nothing said. The command is written to a file
// first, so quotes and newlines in it reach sh unchanged. A failed or cut
// command's summary (the failing tests read from its junit-*.xml) goes to the
// console and the build description; the results it wrote so far stay in
// ci-results, which runUnit stashes and archives. Returns the command's exit
// status (124: cut by the bound). Only runUnit's units call it.
int unitSh(Map m) {
  writeFile file: '.unit-command', text: m.script + '\n'
  int rc = sh(returnStatus: true,
              script: 'ci/unitrun.sh "$BOARD_UNIT_NAME" "$BOARD_UNIT_BOUND_SECONDS" "$BOARD_CI_OUT" -- sh -xe .unit-command')
  String summary = "${env.BOARD_CI_OUT}/unit-failure-${slug(env.BOARD_UNIT_NAME)}.txt"
  if (rc != 0 && fileExists(summary)) {
    String text = readFile(summary).trim()
    echo text
    currentBuild.description = (currentBuild.description ?: '') + ' | ' + text.split('\n').take(5).join('; ')
  }
  return rc
}

// Runs one unit on int21h-ws (AMD; every attempt asks for the same label,
// which matches that one node). u: name, minutes (the attempt's hard limit),
// body (closure on the node, returns the command's exit status), needs
// (stash names to unstash first), extraStash (closure on the node, after a
// run), maxAttempts (default 4).
def runUnit(Map u) {
  def attempts = []
  attemptLog[u.name] = attempts
  int infra = 0
  int maxAttempts = u.maxAttempts ?: 4
  while (true) {
    def a = [n: attempts.size() + 1, label: AMD, node: null, queued: nowMs(), start: 0L, end: 0L, outcome: null]
    attempts << a
    // The job (branch) is part of the id: build numbers repeat across branches,
    // and PR-1 #3's cleanup once removed PR-2 #3's running E2E container.
    String runId = "jenkins-${slug(env.JOB_NAME)}-${env.BUILD_NUMBER}-${slug(u.name)}-${a.n}"
    try {
      // Queue wait (no free executor) is capped too: 45 minutes.
      timeout(time: 45 + u.minutes, unit: 'MINUTES') {
        node(AMD) {
          a.node = env.NODE_NAME
          a.start = nowMs()
          echo "unit ${u.name}: attempt ${a.n} of at most ${maxAttempts} on ${env.NODE_NAME}"
          try {
            // One hard limit per attempt, no inner activity timeout: when the
            // hard limit fired first, the orphaned activity timer later found
            // no thread and Jenkins force-killed the whole build (#12).
            timeout(time: u.minutes, unit: 'MINUTES') {
              if (sh(returnStatus: true, script: 'docker info >/dev/null 2>&1') != 0) {
                a.outcome = 'infra:docker'
                return
              }
              checkout scm
              sh 'git clean -ffdxq'
              if (!commitSha) {
                commitSha = sh(returnStdout: true, script: 'git rev-parse HEAD').trim()
                imageVersion = sh(returnStdout: true, script: 'git describe --tags --exact-match HEAD 2>/dev/null || git rev-parse --short=7 HEAD').trim()
              }
              if (sh(returnStatus: true, script: 'ci/jenkins-image.sh') != 0) {
                a.outcome = 'infra:ci-image'
                return
              }
              (u.needs ?: []).each { unstash it }
              // The command's bound (unitSh): the attempt's limit less what
              // checkout and the image build used and 2 minutes for the summary.
              int left = u.minutes * 60 - (int) ((nowMs() - a.start) / 1000) - 120
              withEnv(["RALPHD_RUN_ID=${runId}", "BOARD_CI_SHA=${commitSha}",
                       "BOARD_IMAGE_REVISION=${commitSha}", "BOARD_IMAGE_VERSION=${imageVersion}",
                       "BOARD_UNIT_NAME=${u.name}", "BOARD_UNIT_BOUND_SECONDS=${left > 60 ? left : 60}"]) {
                int rc = u.body()
                // -1: the durable-task wrapper died (agent restart killed its
                // process group), so the command never finished. 124: the
                // command outlived its bound and ci/unitrun.sh cut it: the
                // unit's own timeout, with its results kept.
                a.outcome = rc == 0 ? 'pass' : rc == -1 ? 'infra:process-lost' : rc == 124 ? 'fail:timeout' : "fail:${rc}"
                if (a.outcome != 'pass') {
                  // ci/rerun-decision.sh: exit 0 only for an infra:* outcome.
                  a.rerun = sh(returnStatus: true, script: "ci/rerun-decision.sh '${a.outcome}'") == 0
                }
              }
              stash name: 'res-' + slug(u.name), includes: 'ci-results/**',
                    excludes: 'ci-results/board-image.tar,ci-results/board-image-oci.tar', allowEmpty: true
              stashedUnits[u.name] = true
              a.stashed = true
              if (u.extraStash) {
                u.extraStash()
              }
            }
          } finally {
            // Sibling and Testcontainers containers of this attempt, by exact
            // ID. Bounded: on a lost agent this would wait for it forever.
            try {
              timeout(time: 3, unit: 'MINUTES') {
                sh(returnStatus: true, script: """
                  ids=\$(docker ps -aq --filter label=ralphd.run=${runId})
                  [ -z "\$ids" ] || { echo "removing leftover containers: \$ids"; docker rm -f \$ids; }
                """)
              }
            } catch (Throwable c) {
              echo "unit ${u.name}: container cleanup on ${a.node} skipped: ${c}"
            }
            // An attempt cut off before its stash (its own limit fired, or the
            // agent went away) keeps what its command had written so far: it
            // leaves unit-cut-<unit>.txt (how it was cut), stashes ci-results so
            // Quality and the report read the partial JUnit files and logs and
            // name the unit (ci/qualitycheck's units gate), and archives them too
            // (PR-1 #8 lost a cut E2E's playwright.log and JUnit files). The post
            // report wipes ci-results in this workspace.
            if (!a.stashed && a.outcome == null) {
              try {
                timeout(time: 3, unit: 'MINUTES') {
                  sh(returnStatus: true, script: """
                    mkdir -p ci-results
                    echo "unit ${u.name}: attempt ${a.n} cut at ${u.minutes} min (the attempt's own limit) or lost with its agent; the results it had written so far are kept" > ci-results/unit-cut-${slug(u.name)}.txt
                  """)
                  stash name: 'res-' + slug(u.name), includes: 'ci-results/**',
                        excludes: 'ci-results/board-image.tar,ci-results/board-image-oci.tar', allowEmpty: true
                  stashedUnits[u.name] = true
                  archiveArtifacts artifacts: 'ci-results/*.log, ci-results/junit-*.xml, ci-results/flaky-*.txt, ci-results/unit-cut-*.txt, ci-results/mutation/*.json, ci-results/mutation-units/*/*.json', allowEmptyArchive: true
                }
              } catch (Throwable c) {
                echo "unit ${u.name}: partial results on ${a.node} not kept: ${c}"
              }
            }
          }
        }
      }
    } catch (Throwable e) {
      String why = classify(e, a)
      if (why == null) {
        a.outcome = 'aborted'
        a.end = nowMs()
        throw e
      }
      a.outcome = why
      echo "unit ${u.name}: attempt ${a.n} on ${a.node ?: '(none)'} failed: ${why} (${e})"
    }
    a.end = nowMs()
    if (a.outcome == 'pass') {
      results[u.name] = 'pass'
      return
    }
    if (a.outcome == 'fail:timeout') {
      // Its own bound: failing again would take the same time, so no rerun.
      results[u.name] = 'fail'
      error("${u.name}: timed out after ${u.minutes} minutes on ${a.node}, not retried (" + attemptSummary(attempts) + ')')
    }
    // ci/rerun-decision.sh already answered for an outcome reached on the
    // node. One classified off a node (no node, agent lost: the exception
    // path) has nothing to run it on, and is rerun only if infrastructure.
    boolean again = a.rerun != null ? a.rerun : a.outcome.startsWith('infra')
    if (!again) {
      // A failed command is a result, not an accident: no rerun, no UNSTABLE.
      results[u.name] = 'fail'
      error("${u.name}: failed, not rerun (" + attemptSummary(attempts) + ')')
    }
    infra++
    if (infra >= 3 || attempts.size() >= maxAttempts) {
      results[u.name] = 'fail'
      error("${u.name}: gave up after ${attempts.size()} attempt" + (attempts.size() == 1 ? '' : 's') + ' (' + attemptSummary(attempts) + ')')
    }
  }
}

// "3 infrastructure failures, 1 test failure: int21h-ws infra:no-node, ..."
@NonCPS
String attemptSummary(List attempts) {
  int fails = attempts.count { it.outcome?.startsWith('fail') }
  int infras = attempts.count { it.outcome?.startsWith('infra') }
  def parts = []
  if (infras) {
    parts << "${infras} infrastructure failure" + (infras == 1 ? '' : 's')
  }
  if (fails) {
    parts << "${fails} test failure" + (fails == 1 ? '' : 's')
  }
  return parts.join(', ') + ': ' + attempts.collect { (it.node ?: 'none') + ' ' + it.outcome }.join(', ')
}

// The commit diff-mode mutation starts from (see the header), on the unit's
// node, in its checkout. A failed fetch throws: an infrastructure failure.
String mutationDiffBase() {
  if (env.BRANCH_NAME == 'main') {
    return sh(returnStdout: true, script: 'git rev-parse HEAD~1').trim()
  }
  String target = env.CHANGE_TARGET ?: 'main'
  withCredentials([gitUsernamePassword(credentialsId: 'github-n-orlov', gitToolName: 'Default')]) {
    sh "git fetch --no-tags --quiet origin +refs/heads/${target}:refs/remotes/origin/${target}"
  }
  return sh(returnStdout: true, script: "git merge-base origin/${target} HEAD").trim()
}

// Incremental mutation's reference (see the header): the number of the last
// successful build of this job whose description says mutation(full), or ''
// when none of the builds kept has one.
String mutationReference() {
  def b = currentBuild.previousSuccessfulBuild
  int left = 30
  while (b != null && left > 0) {
    if ((b.description ?: '').contains('mutation(full)')) {
      return b.number.toString()
    }
    b = b.previousSuccessfulBuild
    left--
  }
  return ''
}

// Replaces the build description's mutation(incremental) with text.
def describeMutation(String text) {
  currentBuild.description = currentBuild.description.replace('mutation(incremental)', text)
}

// Incremental mutation's plan (ci/mutation-plan.sh, see the header) as a map
// of its key=value lines, or [mode: 'full', reason: ...] when the Mutation
// plan unit made none. The unit's stash carries the reused reports
// (ci-results/mutation-ref) to the gate.
Map mutationPlan() {
  String groups = FULL_MUTATION.collect { "'" + it.dir + '=' + it.lanes + "'" }.join(' ')
  boolean timer = !currentBuild.getBuildCauses('hudson.triggers.TimerTrigger$TimerTriggerCause').isEmpty()
  String weekly = timer ? 'BOARD_MUTATION_WEEKLY_FULL=yes ' : ''
  def u = [name: 'Mutation plan', minutes: 15, maxAttempts: 2,
           body: {
             copyArtifacts projectName: env.JOB_NAME, selector: specific(env.CI_MUTATION_REF), optional: true,
                           filter: 'ci-results/mutation/*.json, ci-results/mutation/unit-verdict.txt, ci-results/mutation/stryker/mutation.json, ci-results/commit.txt, ci-results/release-candidate.json',
                           target: 'mutation-ref-raw', fingerprintArtifacts: false
             int rc = sh(returnStatus: true, script: weekly + 'ci/run.sh ci/mutation-plan.sh mutation-ref-raw ci-results/mutation-ref ci/quality.json ci-results/mutation-plan.txt ' + groups)
             if (rc == 0) {
               mutationPlanText = readFile('ci-results/mutation-plan.txt')
             }
             return rc
           }]
  stage(u.name) {
    // No plan is no failure: the build runs in full instead.
    catchError(buildResult: 'SUCCESS', stageResult: 'FAILURE', catchInterruptions: false) {
      runUnit(u)
    }
  }
  if (!mutationPlanText) {
    return [mode: 'full', reason: 'the Mutation plan unit made no plan']
  }
  def plan = [:]
  mutationPlanText.split('\n').each { line ->
    int i = line.indexOf('=')
    if (i > 0) {
      plan[line.substring(0, i)] = line.substring(i + 1)
    }
  }
  return plan
}

// Full mutation's units, then the gate over their union (see the header).
// With an incremental plan, only the units it gives work run, each over the
// lane list it names, and the gate also merges the reports it reused.
def runFullMutation(Map plan) {
  def selected = FULL_MUTATION.findAll { plan == null || plan['group.' + it.dir] }
  // Thread budget (see the header): nproc/3 workers per unit, nproc/2 when
  // at most two run. Stryker (lane base's unit, after its gremlins) gets half
  // the threads, beside the other units' gremlins.
  String share = selected.size() > 2 ? '3' : '2'
  String fullTune = MUTATION_MEMORY + ' BOARD_MUTATION_WORKERS=$(( $(nproc) / ' + share + ' )) BOARD_MUTATION_TEST_CPU=2' +
                    ' BOARD_MUTATION_STRYKER_CONCURRENCY=$(( $(nproc) / 2 ))'
  def branches = [:]
  selected.each { m ->
    String lanes = plan == null ? m.lanes : plan['group.' + m.dir]
    def u = [name: m.name, minutes: 110, maxAttempts: 1,
             body: { unitSh(script: fullTune + " ci/run.sh ci/mutation.sh ci-results/mutation-units/${m.dir} ci/quality.json '${lanes}'") }]
    // 4 executors: Stability holds one, the three units the rest.
    branches[u.name] = unitBranch(u)
  }
  if (branches) {
    parallel branches
  }
  // Runs whatever the units' results: a unit that left no reports fails here
  // as a missing report of every package it owned, and the CI summary still
  // gets one verdict.
  def gate = [name: 'Mutation gate', minutes: 15,
              body: {
                def stashes = selected.collect { it.name } + (plan == null ? [] : ['Mutation plan'])
                stashes.each { name ->
                  try {
                    unstash 'res-' + slug(name)
                  } catch (Throwable e) {
                    echo "no results stashed for ${name}"
                  }
                }
                def dirs = selected.collect { 'ci-results/mutation-units/' + it.dir } + (plan == null ? [] : ['ci-results/mutation-ref'])
                sh(returnStatus: true, script: 'ci/run.sh ci/mutation-merge.sh ci-results/mutation ci/quality.json ' + dirs.join(' '))
              }]
  stage(gate.name) {
    catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {
      runUnit(gate)
    }
  }
}

// A unit as a parallel branch: its failure fails the stage and the build,
// never the sibling branches.
def unitBranch(Map u) {
  return {
    stage(u.name) {
      catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {
        try {
          runUnit(u)
        } finally {
          if (u.signal) {
            signals[u.signal] = true
          }
        }
      }
    }
  }
}

@NonCPS
String timingReport() {
  def lines = ['| unit | result | attempt | node | queue (min) | run (min) | outcome |',
               '|------|--------|---------|------|-------------|-----------|---------|']
  attemptLog.keySet().sort().each { name ->
    attemptLog[name].each { a ->
      def q = a.start ? String.format('%.1f', (a.start - a.queued) / 60000.0) : String.format('%.1f', ((a.end ?: nowMs()) - a.queued) / 60000.0)
      def r = a.start ? String.format('%.1f', ((a.end ?: nowMs()) - a.start) / 60000.0) : '-'
      lines << "| ${name} | ${results[name] ?: '?'} | ${a.n} | ${a.node ?: '-'} | ${q} | ${r} | ${a.outcome} |"
    }
  }
  return lines.join('\n') + '\n'
}

pipeline {
  agent none

  options {
    ansiColor('xterm')
    timestamps()
    buildDiscarder(logRotator(numToKeepStr: '30'))
    disableConcurrentBuilds()
    // A backstop only: every unit has its own bound. Core (Suite's or E2E's 120
    // minutes at worst, then Quality), the mutation plan, a 110-minute
    // mutation unit after a stability wait, the gate, and Perf; a nightly
    // takes about an hour (incremental) to 130 minutes (full).
    timeout(time: 300, unit: 'MINUTES')
    copyArtifactPermission('/board/release,/board/ci/*')
  }

  parameters {
    booleanParam(name: 'EXTENDED', defaultValue: false,
                 description: 'Extended CI: -race, fresh vulnerability databases, then the stability runs')
    booleanParam(name: 'MUTATION', defaultValue: false,
                 description: 'Mutation testing in diff mode: only the lines changed since the merge base with main (on main: since HEAD~1), after Core (beside Stability if extended)')
    booleanParam(name: 'MUTATION_FULL', defaultValue: false,
                 description: 'Full mutation testing: every package and Stryker in three parallel units and one gate (110-minute limit per unit), as the nightly build of main does on Sundays')
    choice(name: 'MUTATION_MODE', choices: ['incremental', 'full'],
           description: 'Mutation of the nightly of main (the timer, or EXTENDED=true on main): incremental reuses the last full run\'s reports of every package no change since could reach and runs the rest (full on Sundays, with no full run to reuse, or when a shared input changed); full runs every package')
  }

  triggers {
    cron(env.BRANCH_NAME == 'main' ? '17 0 * * *' : '')
  }

  environment {
    BOARD_CI_OUT = 'ci-results'
    BOARD_CI_CACHE_VOLUME = 'board-go-cache-jenkins'
    BOARD_CI_NPM_VOLUME = 'board-npm-cache-jenkins'
    BOARD_CI_RUN_URL = "${env.BUILD_URL}"
    BOARD_CI_BUILD_NAME = "${env.JOB_NAME} #${env.BUILD_NUMBER}"
  }

  stages {
    stage('Core') {
      steps {
        script {
          boolean timer = !currentBuild.getBuildCauses('hudson.triggers.TimerTrigger$TimerTriggerCause').isEmpty()
          boolean nightly = params.EXTENDED || timer
          env.CI_NIGHTLY = nightly ? 'true' : 'false'
          // Mutation's mode (see the header): MUTATION_FULL forces full, the
          // nightly of main runs MUTATION_MODE, MUTATION alone is diff mode.
          boolean nightlyMutation = timer || (params.EXTENDED && env.BRANCH_NAME == 'main')
          String mode = params.MUTATION_FULL ? 'full' : nightlyMutation ? (params.MUTATION_MODE ?: 'incremental') : params.MUTATION ? 'diff' : ''
          if (mode == 'incremental') {
            env.CI_MUTATION_REF = mutationReference()
            if (!env.CI_MUTATION_REF) {
              echo 'incremental mutation: no successful full run to reuse; mutation runs in full'
              mode = 'full'
            }
          }
          env.CI_MUTATION = mode ? 'true' : 'false'
          env.CI_MUTATION_MODE = mode ?: 'diff'
          env.BOARD_CI_GO_EXTRA_FLAGS = nightly ? '-race' : ''
          def lanes = ['core'] + (nightly ? ['stability'] : []) + (env.CI_MUTATION == 'true' ? ["mutation(${env.CI_MUTATION_MODE})"] : [])
          currentBuild.description = 'int21h-ws: ' + lanes.join(' + ') + (nightly ? ' (-race)' : '')
          def units = []
          // gofmt, go vet, tidy drift, golangci-lint.
          units << [name: 'Lint', minutes: 30, body: { sh(returnStatus: true, script: 'ci/run.sh ci/lint.sh') }]
          // Everything but Playwright: the web build and Vitest, the Go passes
          // and the merged files (BOARD_CI_PLAYWRIGHT=skip, ci/suite.sh header).
          units << [name: 'Suite', minutes: nightly ? 120 : 45, signal: 'Suite',
                    body: { unitSh(script: 'BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh') }]
          // Playwright alone (BOARD_CI_PLAYWRIGHT=only): every spec, one worker,
          // run once with no repeat of a failed run. 120 minutes on every build.
          units << [name: 'E2E', minutes: 120, signal: 'E2E',
                    body: { unitSh(script: 'BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh') }]
          // Both platforms cross-built and never pushed; the amd64 image is run
          // and probed.
          units << [name: 'Image', minutes: 30, signal: 'Image',
                    body: {
                      int rc = sh(returnStatus: true, script: 'ci/image-build.sh "$BOARD_CI_OUT/board-image.tar" && ci/image-check.sh "$BOARD_CI_OUT"')
                      if (rc != 0) {
                        return rc
                      }
                      // The release dry run (SPEC 12.5): every asset built at this commit and
                      // checked, nothing published.
                      sh(returnStatus: true, script: 'ci/release-check.sh "$BOARD_CI_OUT"')
                    },
                    extraStash: { stash name: 'image', includes: 'ci-results/board-image.tar,ci-results/board-image-oci.tar', allowEmpty: true }]
          units << [name: 'Allure smoke', minutes: 15,
                    body: {
                      sh(returnStatus: true, script: '''
                        set -e
                        mkdir -p allure-smoke/fixture/allure-results
                        BOARD_ALLURE_FIXTURE_OUT=/w/allure-smoke/fixture/allure-results \
                          ci/run.sh go test -count=1 -run '^TestAllureFormatterWritesResultsForAFixtureRun$' ./features/
                        ALLURE_CACHE_DIR="$HOME/.cache/board-allure" ci/allure-smoke.sh allure-smoke/fixture allure-smoke/report
                      ''')
                    }]
          def branches = [:]
          units.each { u -> branches[u.name] = unitBranch(u) }
          // The quality gates read Suite's coverage, E2E's results and Image's tar: wait for all three.
          branches['Quality'] = {
            stage('Quality') {
              waitUntil(initialRecurrencePeriod: 15000) { signals['Suite'] == true && signals['E2E'] == true && signals['Image'] == true }
              catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {
                env.QUALITY_RESULT = 'failing'
                // A unit that left no stash at all (it never got a node, or its
                // agent was lost before the partial stash) is named here, instead
                // of Quality failing on its missing stash. A unit that failed or
                // was cut has a stash, and Quality's units section names it.
                def lost = ['Suite', 'E2E', 'Image'].findAll { !stashedUnits[it] }
                if (lost) {
                  results['Quality'] = 'fail'
                  error('Quality: no results to read from ' + lost.join(', ') + ' (' +
                        lost.collect { it + ': ' + (results[it] ?: 'no result') + ', ' + (attemptLog[it] ? attemptSummary(attemptLog[it]) : 'no attempt') }.join('; ') + ')')
                }
                runUnit([name: 'Quality', minutes: 30, needs: ['res-suite', 'res-e2e', 'res-image', 'image'],
                         body: {
                           if (env.CI_NIGHTLY == 'true') {
                             sh "ci/run.sh sh -c 'rm -rf /go-cache/trivy/db /go-cache/trivy/java-db /go-cache/xdg-cache/govulncheck'"
                           }
                           // crap-ts and the openapi gates read web/node_modules, which
                           // Suite installed in its own workspace, not this one.
                           int rc = sh(returnStatus: true, script: "ci/run.sh sh -c '[ -d web/node_modules ] || (cd web && npm ci --no-audit --no-fund)'")
                           if (rc != 0) {
                             return rc
                           }
                           sh(returnStatus: true, script: 'ci/run.sh ci/quality.sh "$BOARD_CI_OUT" ci/quality.json --mutation-deferred')
                         }])
                env.QUALITY_RESULT = results['Quality'] == 'fail' ? 'failing' : 'passing'
              }
            }
          }
          parallel branches
        }
      }
    }

    stage('Stability and Mutation') {
      parallel {
        stage('Stability') {
          when {
            beforeAgent true
            expression { env.CI_NIGHTLY == 'true' && currentBuild.currentResult != 'FAILURE' }
          }
          steps {
            script {
              // One unit, so one executor: the three runs side by side in it,
              // each in its own clone of the checkout (its own tree and
              // TMPDIR; Postgres testcontainers get random host ports), each
              // its own stage and log (ci-results/stability/run-<i>/). The
              // unit fails when any run fails, with no rerun of the three runs;
              // stability.sh prints only at the end (11 min each).
              def u = [name: 'Stability runs', minutes: 40,
                       body: {
                         sh '''
                           set -e
                           for i in 1 2 3; do
                             rm -rf "stability-$i" "$BOARD_CI_OUT/stability/run-$i"
                             git clone -q --local --no-checkout . "stability-$i"
                             git -C "stability-$i" checkout -q --detach "$BOARD_CI_SHA"
                             # As the checkout: its remote URL and origin/* refs.
                             git -C "stability-$i" fetch -q --no-tags .. '+refs/remotes/*:refs/remotes/*'
                             git -C "stability-$i" remote set-url origin "$(git remote get-url origin)"
                             mkdir -p "$BOARD_CI_OUT/stability/run-$i"
                           done
                         '''
                         // Run 3 at go test -parallel 1 (ci/stability.sh, BOARD_STABILITY_PARALLEL):
                         // a test racing a wall-clock instant against its queue time fails there every time.
                         def rcs = [:]
                         def runs = [:]
                         // A list, not 1..3: an IntRange is not serializable in CPS.
                         [1, 2, 3].each { i ->
                           runs["Stability ${i}"] = {
                             stage("Stability ${i}") {
                               rcs[i] = sh(returnStatus: true, script: "cd stability-${i} && ${i == 3 ? 'BOARD_STABILITY_PARALLEL=1 ' : ''}TMPDIR=\"\$WORKSPACE/\$BOARD_CI_OUT/stability/run-${i}\" ci/stability.sh 1")
                               if (rcs[i] != 0) {
                                 // The run's stage turns red; the unit judges the build.
                                 catchError(buildResult: 'SUCCESS', stageResult: 'FAILURE') {
                                   error("Stability ${i}: ci/stability.sh exited ${rcs[i]}")
                                 }
                               }
                             }
                           }
                         }
                         parallel runs
                         def failed = [1, 2, 3].findAll { rcs[it] != 0 }
                         echo(failed ? "stability: run ${failed.join(', ')} failed" : 'stability: all three runs passed')
                         return failed ? 1 : 0
                       }]
              unitBranch(u).call()
              env.STABILITY_RESULT = results['Stability runs'] == 'pass' ? 'passing' : 'failing'
            }
          }
        }

        stage('Mutation') {
          when {
            beforeAgent true
            expression { env.CI_MUTATION == 'true' && currentBuild.currentResult != 'FAILURE' }
          }
          steps {
            script {
              // The worker and CPU split is read by ci/mutation.sh before it
              // scrubs BOARD_*, and ci/run.sh forwards only BOARD_* into its
              // container.
              String tune = MUTATION_MEMORY + ' BOARD_MUTATION_WORKERS=$(( $(nproc) / 2 )) BOARD_MUTATION_TEST_CPU=2'
              if (env.CI_MUTATION_MODE == 'incremental') {
                def plan = mutationPlan()
                if (plan.mode == 'incremental') {
                  describeMutation("mutation(incremental: ${plan.run} run, ${plan.reused} reused from #${env.CI_MUTATION_REF})")
                  runFullMutation(plan)
                } else {
                  echo "incremental mutation runs in full: ${plan.reason}"
                  env.CI_MUTATION_MODE = 'full'
                  describeMutation("mutation(full) [${plan.reason}]")
                  runFullMutation(null)
                }
              } else if (env.CI_MUTATION_MODE == 'full') {
                runFullMutation(null)
              } else {
                def u = [name: 'Mutation diff', minutes: 45,
                         body: {
                           mutationBase = mutationDiffBase()
                           currentBuild.description = currentBuild.description.replaceAll('mutation\\(diff[^)]*\\)', "mutation(diff ${mutationBase.take(7)})")
                           sh(returnStatus: true, script: "BOARD_MUTATION_DIFF=${mutationBase} " + tune +
                                                          ' ci/run.sh ci/mutation.sh ci-results/mutation ci/quality.json all')
                         }]
                stage(u.name) {
                  catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {
                    runUnit(u)
                  }
                }
              }
            }
          }
        }
      }
    }

    stage('Perf') {
      when {
        beforeAgent true
        expression { env.CI_NIGHTLY == 'true' && env.STABILITY_RESULT == 'passing' }
      }
      steps {
        script {
          // Perf (SPEC 14, R14): the harness against the real binary, 30
          // minutes (about 4 on int21h-ws). It runs after the stability and
          // mutation units, alone on the agent: its p99 and RSS gates measure
          // the product, not suites or mutants running beside it. Its failure
          // fails the build like any unit's; perf.json is archived with the
          // rest.
          def perf = [name: 'Perf', minutes: 30,
                      body: { sh(returnStatus: true, script: 'ci/run.sh ci/perf.sh "$BOARD_CI_OUT"') }]
          unitBranch(perf).call()
        }
      }
    }
  }

  post {
    always {
      script {
        // The report on one pool node over every unit's stash.
        try {
          timeout(time: 30, unit: 'MINUTES') {
            node(AMD) {
              sh 'rm -rf ci-results ci-summary allure-report previous'
              // Plain lists: a map's key view is not serializable across steps.
              for (String name : attemptLog.collect { it.key }) {
                try {
                  unstash 'res-' + slug(name)
                } catch (Throwable e) {
                  echo "no results stashed for ${name}"
                }
              }
              // A flaky list or a leg-divergence list with a line in it fails
              // the build (ci/flakygate.sh), whatever else the report does.
              catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {
                sh 'ci/flakygate.sh "$BOARD_CI_OUT"'
              }
              writeFile file: 'ci-results/distributed/timing.md', text: timingReport()
              echo timingReport()
              junit testResults: 'ci-results/junit-merged/*.xml', allowEmptyResults: true,
                    skipMarkingBuildUnstable: true, skipPublishingChecks: true
              copyArtifacts projectName: env.JOB_NAME, selector: lastCompleted(), filter: 'allure-history/**',
                            target: 'previous', optional: true, fingerprintArtifacts: false
              catchError(buildResult: 'UNSTABLE', stageResult: 'FAILURE') {
                sh 'ALLURE_CACHE_DIR="$HOME/.cache/board-allure" ci/jenkins-report.sh "$BOARD_CI_OUT" previous/allure-history'
              }
              publishHTML([reportName: 'Allure', reportDir: 'allure-report', reportFiles: 'index.html',
                           keepAll: true, alwaysLinkToLastBuild: true, allowMissing: true])
              publishHTML([reportName: 'CI summary', reportDir: 'ci-summary', reportFiles: 'index.html',
                           keepAll: true, alwaysLinkToLastBuild: true, allowMissing: true])
              def testsBadge = addEmbeddableBadgeConfiguration(id: 'tests', subject: 'tests', status: 'not run', color: 'lightgrey')
              def coverageBadge = addEmbeddableBadgeConfiguration(id: 'coverage', subject: 'coverage', status: 'not run', color: 'lightgrey')
              def qualityBadge = addEmbeddableBadgeConfiguration(id: 'quality', subject: 'quality', status: 'not run', color: 'lightgrey')
              if (env.QUALITY_RESULT) {
                qualityBadge.setStatus(env.QUALITY_RESULT)
                qualityBadge.setColor(env.QUALITY_RESULT == 'passing' ? 'brightgreen' : 'red')
              }
              if (fileExists('ci-summary/counts.txt')) {
                def c = [:]
                readFile('ci-summary/counts.txt').split('\n').each { line ->
                  def i = line.indexOf('=')
                  if (i > 0) {
                    c[line.substring(0, i)] = line.substring(i + 1).trim()
                  }
                }
                if (c.pass) {
                  def text = 'Tests: ' + c.pass + ' passed, ' + c.fail + ' failed, ' + c.skip + ' skipped, ' + c.flaky + ' flaky'
                  if (c.coverage) {
                    text += '; unit coverage ' + c.coverage
                  }
                  def ok = c.fail == '0'
                  testsBadge.setStatus(ok ? c.pass + ' passed' : c.fail + ' failed')
                  testsBadge.setColor(ok ? 'brightgreen' : 'red')
                  if (c.coverage) {
                    def pct = c.coverage.replace('%', '').toDouble()
                    coverageBadge.setStatus(c.coverage)
                    coverageBadge.setColor(pct >= 80 ? 'brightgreen' : pct >= 60 ? 'yellow' : 'red')
                  }
                  addSummary(icon: ok ? 'symbol-checkmark-circle-outline plugin-ionicons-api'
                                      : 'symbol-close-circle-outline plugin-ionicons-api',
                             text: text + ' (<a href="' + env.BUILD_URL + 'Allure/">Allure</a>, ' +
                                   '<a href="' + env.BUILD_URL + 'CI_20summary/">CI summary</a>)')
                }
              }
              addSummary(icon: 'symbol-time-outline plugin-ionicons-api',
                         text: 'Distributed: ' + attemptLog.size() + ' units on ' +
                               attemptLog.values().flatten().collect { it.node }.findAll { it }.unique().sort().join(', ') +
                               ' (<a href="' + env.BUILD_URL + 'artifact/ci-results/distributed/timing.md">timing</a>)')
              // The commit, for a later incremental mutation run that reuses
              // this build's reports (ci/mutation-plan.sh).
              if (commitSha) {
                writeFile file: 'ci-results/commit.txt', text: commitSha + '\n'
              }
              archiveArtifacts artifacts: 'ci-results/**/*.txt, ci-results/**/*.xml, ci-results/**/*.out, ci-results/*.cdx.json, ci-results/mutation/*.json, ci-results/mutation/stryker/mutation.json, ci-results/stability/**/*.log, ci-results/perf.json, ci-results/distributed/**, ci-summary/**, allure-history/**',
                               allowEmptyArchive: true
              if (currentBuild.currentResult == 'SUCCESS' && env.BRANCH_NAME == 'main') {
                writeFile file: 'ci-results/release-candidate.json',
                          text: '{"commit": "' + commitSha + '", "build_url": "' + env.BUILD_URL +
                                '", "build_number": ' + env.BUILD_NUMBER + '}\n'
                archiveArtifacts artifacts: 'ci-results/release-candidate.json', fingerprint: true
              }
            }
          }
        } catch (Throwable e) {
          echo "report failed: ${e}"
          echo timingReport()
        }
      }
    }
    cleanup {
      script {
        def watched = env.BRANCH_NAME == 'main' || env.CI_NIGHTLY == 'true' || env.CI_MUTATION == 'true'
        // agent none: notifyTelegram's sh needs a workspace.
        try {
          timeout(time: 10, unit: 'MINUTES') {
            node(AMD) {
              notifyTelegram(notifyOn: 'ALL', silent: watched ? null : true,
                             extra: 'distributed' + (env.CI_NIGHTLY == 'true' ? ', extended lane' : '') +
                                    (env.CI_MUTATION == 'true' ? ", mutation(${env.CI_MUTATION_MODE})" : ''))
            }
          }
        } catch (Throwable e) {
          echo "notifyTelegram: no node: ${e}"
        }
      }
    }
  }
}
