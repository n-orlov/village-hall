Feature: The check-in from the command line
  vh now prints who I am and six sections, notices, my leases, others, conflicts, my
  asks and notes, one line each, then an attention line with the counts. A
  conflict line starts with CONFLICT and an answered ask of mine starts with
  ANSWERED. It exits 7 when a conflict touches one of my live leases or a
  resource given with --resources, else 0.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I run the client 'vh init add aa "First one"' as actor "claude@laptop"
    And 3 tasks exist in the initiative "aa" as actor "claude@laptop"

  Scenario: with no leases every section is empty and the exit code is 0
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stderr is empty
    And stdout has 8 lines
    And stdout line 1 matches '^you: claude@laptop$'
    And stdout line 2 matches '^notices: none$'
    And stdout line 3 matches '^my leases: none$'
    And stdout line 4 matches '^others: none$'
    And stdout line 5 matches '^conflicts: none$'
    And stdout line 6 matches '^my asks: none$'
    And stdout line 7 matches '^notes: none$'
    And stdout line 8 matches '^attention: asks 0 · done 0 · expired 0 · overdue 0 · conflicts 0$'

  Scenario: my lease is listed under my leases only
    Given I run the client 'vh lease start aa-1 "wiring" --ttl 3h --res repo:x' as actor "claude@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has 9 lines
    And stdout line 3 matches '^my leases:$'
    And stdout line 4 matches '^  L1  aa-1  Task 1  "wiring"  res repo:x  exp (3h|2h59m)$'
    And stdout line 5 matches '^others: none$'
    And stdout line 6 matches '^conflicts: none$'

  Scenario: a notice is listed under notices for everyone and not under others
    Given I run the client 'vh lease start aa-2 "gw1 reboots at noon" --res host:gw1 --notice' as actor "codex@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 2 matches '^notices:$'
    And stdout line 3 matches '^  L1  aa-2  codex@laptop  "gw1 reboots at noon"  res host:gw1  exp (2h|1h59m)$'
    And stdout line 4 matches '^my leases: none$'
    And stdout line 5 matches '^others: none$'
    When I run the client 'vh lease ls --notice' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has 1 lines
    And stdout line 1 matches '^L1  live  aa-2  codex@laptop  "gw1 reboots at noon"  res host:gw1  exp (2h|1h59m)$'

  Scenario: a lease of another actor is listed under others only
    Given I run the client 'vh lease start aa-2 "testing" --res repo:y' as actor "codex@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 3 matches '^my leases: none$'
    And stdout line 4 matches '^others:$'
    And stdout line 5 matches '^  L1  aa-2  codex@laptop  "testing"  res repo:y  exp (2h|1h59m)$'
    And stdout line 6 matches '^conflicts: none$'

  Scenario: a conflict on my lease prints a CONFLICT line and exits 7
    Given I run the client 'vh lease start aa-2 "testing" --res repo:y' as actor "codex@laptop"
    And I run the client 'vh lease start aa-1 "wiring" --res repo:y --res repo:z' as actor "claude@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 7
    And stderr is empty
    And stdout line 3 matches '^my leases:$'
    And stdout line 4 matches '^  L2  aa-1  Task 1  "wiring"  res repo:y,repo:z  exp '
    And stdout line 5 matches '^others:$'
    And stdout line 6 matches '^  L1  aa-2  codex@laptop  "testing"  res repo:y  exp '
    And stdout line 7 matches '^conflicts:$'
    And stdout line 8 matches '^  CONFLICT repo:y: L1 codex@laptop \(aa-2\) "testing" exp '

  Scenario: a conflict on a resource given with --resources exits 7 and without it exits 0
    Given I run the client 'vh lease start aa-2 "testing" --res host:gw1' as actor "codex@laptop"
    When I run the client 'vh now --resources host:gw1,repo:q' as actor "claude@laptop"
    Then the exit code is 7
    And stdout line 3 matches '^my leases: none$'
    And stdout line 7 matches '^  CONFLICT host:gw1: L1 codex@laptop \(aa-2\) "testing" exp '
    When I run the client 'vh now --resources repo:q' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 6 matches '^conflicts: none$'
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    When I run the client 'vh now --resources=host:gw1' as actor "claude@laptop"
    Then the exit code is 7

  Scenario: no conflict exits 0 when leases hold different resources
    Given I run the client 'vh lease start aa-2 "testing" --res repo:y' as actor "codex@laptop"
    And I run the client 'vh lease start aa-1 "wiring" --res repo:x' as actor "claude@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 7 matches '^conflicts: none$'

  Scenario: --json prints the answer of the server and keeps the exit code
    Given I run the client 'vh lease start aa-2 "testing" --res repo:y' as actor "codex@laptop"
    And I run the client 'vh lease start aa-1 "wiring" --res repo:y' as actor "claude@laptop"
    When I run the client 'vh now --json' as actor "claude@laptop"
    Then the exit code is 7
    And stdout JSON path "actor" is "claude@laptop"
    And stdout JSON path "my_leases" has 1 items
    And stdout JSON path "my_leases.0.title" is "Task 1"
    And stdout JSON path "others_leases" has 1 items
    And stdout JSON path "conflicts" has 1 items
    And stdout JSON path "conflicts.0.lease" is "L1"
    When I run the client 'vh now --json --resources repo:none' as actor "claude@laptop"
    Then the exit code is 7
    When I run the client 'vh lease end L2' as actor "claude@laptop"
    And I run the client 'vh now --json' as actor "claude@laptop"
    Then the exit code is 0
    And stdout JSON path "my_leases" has 0 items
    And stdout JSON path "conflicts" has 0 items

  Scenario: a section shows five lines and then how to see the rest
    Given 7 tasks exist in the initiative "aa" as actor "other@laptop"
    And I run the client 'vh lease start aa-4 "a"' as actor "codex@laptop"
    And I run the client 'vh lease start aa-5 "b"' as actor "codex@laptop"
    And I run the client 'vh lease start aa-6 "c"' as actor "codex@laptop"
    And I run the client 'vh lease start aa-7 "d"' as actor "codex@laptop"
    And I run the client 'vh lease start aa-8 "e"' as actor "codex@laptop"
    And I run the client 'vh lease start aa-9 "f"' as actor "codex@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has 14 lines
    And stdout line 5 matches '^  L1  aa-4  codex@laptop  "a"'
    And stdout line 9 matches '^  L5  aa-8  codex@laptop  "e"'
    And stdout line 10 matches '^  \+1 more → vh lease ls$'
    And stdout line 11 matches '^conflicts: none$'

  Scenario: my asks lists an open ask and an answered one with ANSWERED and the answer
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    And I run the client 'vh ask aa-2 "Ship it?" --blocking' as actor "claude@laptop"
    And I run the client 'vh ask aa-3 "Somebody else asks"' as actor "codex@laptop"
    And I run the client 'vh answer A2 "yes, ship"' as actor "operator@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stderr is empty
    And stdout line 6 matches '^my asks:$'
    And stdout line 7 matches '^  A1  open  aa-1  claude@laptop  "Which port\?"  asked (just now|\d+m ago)$'
    And stdout line 8 matches '^  ANSWERED A2  answered  aa-2  claude@laptop  "Ship it\?"  \(blocking\)  answered by operator@laptop (just now|\d+m ago): "yes, ship"$'
    And stdout line 9 matches '^notes: none$'
    And stdout line 10 matches '^attention: asks 2 · done 0 · expired 0 · overdue 0 · conflicts 0$'
    And stdout has 10 lines

  Scenario: another actor sees none of my asks
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    When I run the client 'vh now' as actor "codex@laptop"
    Then stdout line 6 matches '^my asks: none$'
    And stdout line 8 matches '^attention: asks 1 '

  Scenario: notes lists the notes that concern me and leaves out the others
    Given I run the client 'vh lease start aa-1 "wiring"' as actor "claude@laptop"
    And I run the client 'vh note "on my task" --task aa-1' as actor "codex@laptop"
    And I run the client 'vh note "on another task" --task aa-2' as actor "codex@laptop"
    And I run the client 'vh note "psst" --init aa --mention claude@laptop' as actor "codex@laptop"
    And the standard input holds "the details"
    And I run the client 'vh note "everyone listen" --body -' as actor "codex@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 7 matches '^my asks: none$'
    And stdout line 8 matches '^notes:$'
    And stdout line 9 matches '^  N1  (just now|\d+m ago)  codex@laptop  aa-1  on my task$'
    And stdout line 10 matches '^  N3  (just now|\d+m ago)  codex@laptop  aa  psst$'
    And stdout line 11 matches '^  N4  (just now|\d+m ago)  codex@laptop  broadcast  everyone listen  \+$'
    And stdout line 12 matches '^attention: asks 0 '
    And stdout has 12 lines
    When I run the client 'vh notes --since last --ack' as actor "claude@laptop"
    And I run the client 'vh now' as actor "claude@laptop"
    Then stdout line 8 matches '^notes: none$'

  Scenario: the notes section shows five lines and then how to see the rest
    Given I run the client 'vh note "n1"' as actor "codex@laptop"
    And I run the client 'vh note "n2"' as actor "codex@laptop"
    And I run the client 'vh note "n3"' as actor "codex@laptop"
    And I run the client 'vh note "n4"' as actor "codex@laptop"
    And I run the client 'vh note "n5"' as actor "codex@laptop"
    And I run the client 'vh note "n6"' as actor "codex@laptop"
    And I run the client 'vh note "n7"' as actor "codex@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then stdout line 7 matches '^notes:$'
    And stdout line 8 matches '^  N1 '
    And stdout line 12 matches '^  N5 '
    And stdout line 13 matches '^  \+2 more → vh notes --since last$'
    And stdout line 14 matches '^attention: '

  Scenario: the attention line counts what needs the operator
    Given I run the client 'vh lease start aa-2 "testing" --res repo:y' as actor "codex@laptop"
    And I run the client 'vh lease start aa-1 "wiring" --res repo:y' as actor "claude@laptop"
    And I run the client 'vh ask aa-3 "Which port?"' as actor "codex@laptop"
    And I run the client 'vh task done aa-3' as actor "codex@laptop"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 7
    And stdout line 11 matches '^attention: asks 1 · done 1 · expired 0 · overdue 0 · conflicts 1$'

  Scenario: --json carries the new parts and the exit code does not change
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    And I run the client 'vh note "everyone listen"' as actor "codex@laptop"
    When I run the client 'vh now --json' as actor "claude@laptop"
    Then the exit code is 0
    And stdout JSON path "my_asks" has 1 items
    And stdout JSON path "my_asks.0.ref" is "A1"
    And stdout JSON path "notes" has 1 items
    And stdout JSON path "attention.open_asks" is "1"

  Scenario: a malformed command line exits 2 and a resource the server refuses exits 1
    When I run the client 'vh now extra' as actor "claude@laptop"
    Then the exit code is 2
    And stderr is exactly 'usage: vh now [--resources r1,r2] [--json]'
    When I run the client 'vh now --bogus' as actor "claude@laptop"
    Then the exit code is 2
    When I run the client 'vh now --resources a:b,,c:d' as actor "claude@laptop"
    Then the exit code is 1
    And stdout is empty
    When I run the client 'vh now --resources ""' as actor "claude@laptop"
    Then the exit code is 0
