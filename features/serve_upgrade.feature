@serve-upgrade
Feature: Starting the server on a Phase 2 database
  A task is never done while it holds a live lease (R14), but a Phase 2 database
  can hold one. `vh serve` reopens each such done task when it starts: the
  task goes back to open and one task_reopened event records it, made by the
  holder of the live lease, under the principal system:upgrade, with the reason
  "live lease at upgrade". Each scenario runs on the leg BOARD_DB_LEG names.

  Scenario: starting the server reopens a done task that holds a live lease, once
    Given a Phase 2 database holding the done task "aa-1" with a live lease held by "claude@laptop"
    When the board server is running on a free port
    Then the task "aa-1" is open
    And the task "aa-1" has exactly 1 task_reopened event, made by "claude@laptop" under principal "system:upgrade" with the reason "live lease at upgrade"
    When the board server is stopped and started again
    Then the task "aa-1" is open
    And the task "aa-1" has exactly 1 task_reopened event, made by "claude@laptop" under principal "system:upgrade" with the reason "live lease at upgrade"

  Scenario: starting the server leaves a done task whose lease has ended alone
    Given a Phase 2 database holding the done task "aa-1" whose lease ended
    When the board server is running on a free port
    Then the task "aa-1" is done
    And the task "aa-1" has exactly 0 task_reopened events
