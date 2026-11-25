Feature: Starting work from the command line, vh start
  vh start <task> "<intent>" starts or renews the caller's lease and checks in with one request. It prints the
  ok line of vh lease start, then one CONFLICT line for each live lease of another that holds a resource of
  the lease, then the now summary. It exits 0, or 7 when it printed a CONFLICT line, 6 for a done or closed task,
  4 for an unknown task and 2 for a malformed command line. A refusal writes nothing.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I run the client 'vh init add aa "First one"' as actor "claude@laptop"
    And 3 tasks exist in the initiative "aa" as actor "claude@laptop"

  Scenario: a new lease prints the lease line and then the now summary
    Given I note the counts of tasks, leases and events
    When I run the client 'vh start aa-1 "fixing it" --ttl 4h --res repo:x' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 1 matches '^ok lease L1 aa-1 exp 4h$'
    And stdout line 2 matches '^you: claude@laptop$'
    And stdout line 3 matches '^my leases:$'
    And stdout line 4 matches '^  L1  aa-1  Task 1  "fixing it"  res repo:x  exp '
    And stderr is empty
    And the counts of tasks, leases and events changed by 0, 1 and 1
    And exactly 1 lease_started event exists for the lease "L1"

  Scenario: starting again on the same task renews the lease and prints the same lines
    Given I run the client 'vh start aa-1 "first" --ttl 1h' as actor "claude@laptop"
    And I note the counts of tasks, leases and events
    When I run the client 'vh start aa-1 "second" --ttl 3h' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 1 matches '^ok lease L1 aa-1 exp 3h$'
    And stdout line 2 matches '^you: claude@laptop$'
    And the counts of tasks, leases and events changed by 0, 0 and 1
    And exactly 1 lease_renewed event exists for the lease "L1"

  Scenario: a shared resource is a conflict, the lease is written and the exit code is 7
    Given I run the client 'vh start aa-1 "migrate" --res db:x' as actor "claude@laptop"
    When I run the client 'vh start aa-2 "reindex" --res db:x' as actor "codex@laptop"
    Then the exit code is 7
    And stdout line 1 matches '^ok lease L2 aa-2 exp 2h$'
    And stdout line 2 matches '^CONFLICT db:x: L1 claude@laptop \(aa-1\) "migrate" exp '
    And stdout line 3 matches '^you: codex@laptop$'
    And stdout has the line "attention: asks 0 · done 0 · expired 0 · overdue 0 · conflicts 1"
    And stderr is empty
    And there are 2 leases in the database

  Scenario: --json prints the response with the lease, the now summary and the protocol versions
    When I run the client 'vh start aa-1 "fixing it" --res repo:x --json' as actor "claude@laptop"
    Then the exit code is 0
    And stdout JSON path "lease.ref" is "L1"
    And stdout JSON path "lease.resources.0" is "repo:x"
    And stdout JSON path "conflicts" has 0 items
    And stdout JSON path "task_created" is "false"
    And stdout JSON path "now.my_leases" has 1 items
    And stdout JSON path "protocol_seen" is "0"

  Scenario: a done or closed task exits 6 and writes nothing
    Given I run the client 'vh task done aa-2' as actor "claude@laptop"
    And I run the client 'vh task close aa-3' as actor "claude@laptop"
    And I note the counts of tasks, leases and events
    When I run the client 'vh start aa-2 "x"' as actor "codex@laptop"
    Then the exit code is 6
    And stdout is empty
    And stderr is one line ending with "→ vh task reopen aa-2 first"
    When I run the client 'vh start aa-3 "x"' as actor "codex@laptop"
    Then the exit code is 6
    And stdout is empty
    And stderr is one line ending with "→ vh task show aa-3"
    And the counts of tasks, leases and events changed by 0, 0 and 0

  Scenario: an unknown task exits 4 and writes nothing
    Given I note the counts of tasks, leases and events
    When I run the client 'vh start zz-1 "x"' as actor "codex@laptop"
    Then the exit code is 4
    And stdout is empty
    And stderr is one line ending with "→ vh task ls"
    And the counts of tasks, leases and events changed by 0, 0 and 0

  Scenario Outline: a malformed command line exits 2 with one usage line and sends nothing
    Given I note the counts of tasks, leases and events
    When I run the client '<command>' as actor "claude@laptop"
    Then the exit code is 2
    And stdout is empty
    And stderr is one line ending with '<line>'
    And the counts of tasks, leases and events changed by 0, 0 and 0

    Examples:
      | command                          | line                                                                                 |
      | vh start                         | usage: vh start <task> "<intent>" [--res r]... [--ttl D] [--notice] [--json]          |
      | vh start aa-1                    | usage: vh start <task> "<intent>" [--res r]... [--ttl D] [--notice] [--json]          |
      | vh start aa-1 x extra            | usage: vh start <task> "<intent>" [--res r]... [--ttl D] [--notice] [--json]          |
      | vh start aa-1 x --start y        | usage: vh start <task> "<intent>" [--res r]... [--ttl D] [--notice] [--json]          |
      | vh start aa-1 x --ttl 2          | invalid duration "2": want a whole number and a unit s, m, h or d, such as 90m       |

  Scenario: a server with a newer protocol than the binary makes start print the update hint and keeps exit 0
    Given a stub board answers vh start with protocol 99 and protocol_seen 4
    When I run the client 'vh start aa-1 "x"' as actor "claude@laptop" against the stub board
    Then the exit code is 0
    And stdout has the line "hint: vh protocol v99 on the server, you have v5 → vh update, then vh help agent"
    And the last stdout line starts with "hint: "
    And stdout has 1 line starting with "hint: "
    And stderr is empty

  Scenario: a protocol_seen older than the binary's makes start and new print the read-again hint and keep their exit codes
    When I run the client 'vh start aa-1 "x"' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has the line "hint: vh protocol v5 changed since you last read it → vh help agent"
    And the last stdout line starts with "hint: "
    And stdout has 1 line starting with "hint: "
    And stderr is empty
    When I run the client 'vh new aa "Fourth" --start "y" --res db:x' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has the line "hint: vh protocol v5 changed since you last read it → vh help agent"
    And stdout has 1 line starting with "hint: "
    When I run the client 'vh start aa-2 "z" --res db:x' as actor "codex@laptop"
    Then the exit code is 7
    And stdout has 1 line starting with "hint: "

  Scenario: an actor that has read the current protocol sees no hint
    Given I run the client 'vh help agent' as actor "claude@laptop"
    When I run the client 'vh start aa-1 "x"' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has no line starting with "hint: "
    And stderr is empty
