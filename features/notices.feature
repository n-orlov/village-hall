Feature: A notice from start to end, seen by another actor
  A notice is a live lease that tells everyone what is going on, started with --notice. It is listed
  under notices in vh now of every actor, renewed like a lease and gone from notices once it is ended.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I run the client 'vh init add aa "First one"' as actor "claude@laptop"
    And 3 tasks exist in the initiative "aa" as actor "claude@laptop"

  Scenario: a notice is started by one actor, seen by another, renewed and ended
    When I run the client 'vh lease start aa-2 "gw1 reboots at noon" --res host:gw1 --notice --ttl 1h' as actor "codex@laptop"
    Then the exit code is 0
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 2 matches '^notices:$'
    And stdout line 3 matches '^  L1  aa-2  codex@laptop  "gw1 reboots at noon"  res host:gw1  exp (1h|59m)$'
    And stdout line 4 matches '^my leases: none$'
    And stdout line 5 matches '^others: none$'
    When I run the client 'vh lease renew L1 --ttl 5h --intent "gw1 reboots at one"' as actor "codex@laptop"
    Then the exit code is 0
    And stdout is exactly "ok lease L1 aa-2 exp 5h"
    And exactly 1 lease_renewed event exists for the lease "L1"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 2 matches '^notices:$'
    And stdout line 3 matches '^  L1  aa-2  codex@laptop  "gw1 reboots at one"  res host:gw1  exp (5h|4h59m)$'
    And stdout line 5 matches '^others: none$'
    When I run the client 'vh lease end L1' as actor "codex@laptop"
    Then the exit code is 0
    And stdout is exactly "ok lease L1 ended"
    When I run the client 'vh now' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 2 matches '^notices: none$'
    And stdout line 3 matches '^my leases: none$'
