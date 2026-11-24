Feature: Raising an ask and stopping or waiting from the command line, vh block
  vh block <task> "<question>" [--poll D | --stop] raises an ask on the task with one request. The default, --stop,
  ends the caller's lease on the task as blocked on the ask and prints the ok line of vh ask and then that of
  vh lease end. --poll D keeps the lease, renews it while it waits and exits 0, 6 or 3 as vh ask --poll does.
  It exits 4 for an unknown task, 6 for a closed one and 2 for a malformed command line. A refusal writes nothing.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I run the client 'vh init add aa "First one"' as actor "claude@laptop"
    And 2 tasks exist in the initiative "aa" as actor "claude@laptop"

  Scenario: the default stop with a live lease prints the ask line and then the lease line
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    And I note the counts of asks, events and live leases
    When I run the client 'vh block aa-1 "Which port?"' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is:
      """
      ok ask A1 raised
      ok lease L1 ended
      """
    And stderr is empty
    And the counts of asks, events and live leases changed by 1, 2 and -1
    And the lease "L1" is "ended" in the database
    And the lease "L1" has the outcome "blocked on A1" in the database
    And the ask "A1" is "open" in the database

  Scenario: an explicit --stop does the same
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    When I run the client 'vh block aa-1 "Which port?" --stop' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is:
      """
      ok ask A1 raised
      ok lease L1 ended
      """
    And the lease "L1" is "ended" in the database

  Scenario: stop without a lease prints only the ask line
    Given I run the client 'vh start aa-1 "theirs"' as actor "codex@laptop"
    And I note the counts of asks, events and live leases
    When I run the client 'vh block aa-1 "Which port?"' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is exactly "ok ask A1 raised"
    And stderr is empty
    And the counts of asks, events and live leases changed by 1, 1 and 0
    And the lease "L1" is "live" in the database

  Scenario: --json prints the response with the ended lease
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    When I run the client 'vh block aa-1 "Which port?" --json' as actor "claude@laptop"
    Then the exit code is 0
    And stdout JSON path "ref" is "A1"
    And stdout JSON path "lease.ref" is "L1"
    And stdout JSON path "lease.outcome" is "blocked on A1"

  Scenario: --poll prints the answer given while it waits, keeps and renews the lease and exits 0
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    When I start the client 'vh block aa-1 "Which port?" --poll 30s' as actor "claude@laptop" in the background
    And within 10 seconds the poll of the ask "A1" is seen in the database
    Then the background client is still waiting
    And the lease "L1" is "live" in the database
    When I run the client 'vh answer A1 "8080"' as actor "nik@phone"
    Then the exit code is 0
    When the background client ends within 10 seconds
    Then the exit code is 0
    And stdout is:
      """
      ok ask A1 raised
      ok A1 answered by nik@phone: 8080
      """
    And stderr is empty
    And the lease "L1" is "live" in the database
    And exactly 1 lease_renewed event exists for the lease "L1"
    And exactly 0 lease_ended events exist for the lease "L1"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "mode" is "poll"
    And stdout JSON path "state" is "answered"

  Scenario: --poll prints the withdrawal and exits 6
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    When I start the client 'vh block aa-1 "Which port?" --poll 30s' as actor "claude@laptop" in the background
    And within 10 seconds the poll of the ask "A1" is seen in the database
    And I run the client 'vh ask withdraw A1 "moot"' as actor "claude@laptop"
    And the background client ends within 10 seconds
    Then the exit code is 6
    And stdout is:
      """
      ok ask A1 raised
      A1 withdrawn: moot
      """
    And stderr is empty
    And the lease "L1" is "live" in the database

  Scenario: --poll with a short deadline exits 3 and leaves the ask open and the lease live
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    When I run the client 'vh block aa-1 "Which port?" --poll 1s' as actor "claude@laptop"
    Then the exit code is 3
    And stdout is:
      """
      ok ask A1 raised
      A1 still open → vh ask show A1
      """
    And stderr is empty
    And the ask "A1" is "open" in the database
    And the lease "L1" is "live" in the database
    And exactly 1 lease_renewed event exists for the lease "L1"

  Scenario: --poll without a lease prints the same lines and renews nothing
    When I run the client 'vh block aa-1 "Which port?" --poll 1s' as actor "claude@laptop"
    Then the exit code is 3
    And stdout is:
      """
      ok ask A1 raised
      A1 still open → vh ask show A1
      """
    And there are 0 leases in the database

  Scenario Outline: an unknown or closed task exits 4 or 6 and writes nothing
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    And I run the client 'vh task close aa-2' as actor "claude@laptop"
    And I note the counts of asks, events and live leases
    When I run the client '<command>' as actor "claude@laptop"
    Then the exit code is <code>
    And stdout is empty
    And the counts of asks, events and live leases changed by 0, 0 and 0
    And the lease "L1" is "live" in the database

    Examples:
      | command                                | code |
      | vh block zz-9 "Which port?"            | 4    |
      | vh block zz-9 "Which port?" --poll 2s  | 4    |
      | vh block aa-2 "Which port?"            | 6    |
      | vh block aa-2 "Which port?" --poll 2s  | 6    |

  Scenario Outline: a malformed command line exits 2 with one usage line and sends nothing
    Given I run the client 'vh start aa-1 "working"' as actor "claude@laptop"
    And I note the counts of asks, events and live leases
    When I run the client '<command>' as actor "claude@laptop"
    Then the exit code is 2
    And stdout is empty
    And stderr is one line ending with '<line>'
    And the counts of asks, events and live leases changed by 0, 0 and 0
    And the lease "L1" is "live" in the database

    Examples:
      | command                                   | line                                                              |
      | vh block aa-1 "q" --poll 5m --stop        | usage: vh block <task> "<question>" [--poll D \| --stop] [--json] |
      | vh block aa-1 "q" --poll soon             | usage: vh block <task> "<question>" [--poll D \| --stop] [--json] |
      | vh block aa-1                             | usage: vh block <task> "<question>" [--poll D \| --stop] [--json] |
      | vh block                                  | usage: vh block <task> "<question>" [--poll D \| --stop] [--json] |
      | vh block aa-1 "q" extra                   | usage: vh block <task> "<question>" [--poll D \| --stop] [--json] |
      | vh block aa-1 "q" --no-such               | usage: vh block <task> "<question>" [--poll D \| --stop] [--json] |
