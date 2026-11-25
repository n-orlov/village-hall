Feature: Creating and starting a task from the command line, vh new
  vh new <initiative> "<title>" --start "<intent>" creates the task, starts the caller's lease on it and checks
  in with one request. It prints the ok line of vh task add, then the ok line of vh lease start with its CONFLICT
  lines, then the now summary. It exits 0, or 7 when it printed a CONFLICT line, 6 for a closed initiative, 4 for
  an unknown initiative and 2 for a malformed command line, --start missing included. A refusal creates no task.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I run the client 'vh init add aa "First one"' as actor "claude@laptop"

  Scenario: a new task and its lease print the task line, the lease line and then the now summary
    Given I note the counts of tasks, leases and events
    When I run the client 'vh new aa "Fresh work" --start "doing it" --ttl 90m --res repo:z' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 1 matches '^ok task aa-1 created$'
    And stdout line 2 matches '^ok lease L1 aa-1 exp 1h30m$'
    And stdout line 3 matches '^you: claude@laptop$'
    And stdout line 4 matches '^notices: none$'
    And stdout line 5 matches '^my leases:$'
    And stderr is empty
    And the counts of tasks, leases and events changed by 1, 1 and 2
    And exactly 1 task_created event exists for the task "aa-1"
    And exactly 1 lease_started event exists for the task "aa-1"

  Scenario: a shared resource is a conflict, the task and lease are written and the exit code is 7
    Given I run the client 'vh new aa "First" --start "migrate" --res db:x' as actor "claude@laptop"
    When I run the client 'vh new aa "Second" --start "reindex" --res db:x' as actor "codex@laptop"
    Then the exit code is 7
    And stdout line 1 matches '^ok task aa-2 created$'
    And stdout line 2 matches '^ok lease L2 aa-2 exp 2h$'
    And stdout line 3 matches '^CONFLICT db:x: L1 claude@laptop \(aa-1\) "migrate" exp '
    And stdout line 4 matches '^you: codex@laptop$'
    And stderr is empty

  Scenario: --json prints the response with task_created true
    When I run the client 'vh new aa "Fresh work" --start "doing it" --json' as actor "claude@laptop"
    Then the exit code is 0
    And stdout JSON path "task_created" is "true"
    And stdout JSON path "lease.task" is "aa-1"
    And stdout JSON path "now.my_leases.0.title" is "Fresh work"

  Scenario: a closed initiative exits 6 and creates no task
    Given I run the client 'vh init close aa' as actor "claude@laptop"
    And I note the counts of tasks, leases and events
    When I run the client 'vh new aa "Late" --start "x"' as actor "codex@laptop"
    Then the exit code is 6
    And stdout is empty
    And stderr is one line ending with "→ vh init show aa"
    And the counts of tasks, leases and events changed by 0, 0 and 0

  Scenario: an unknown initiative exits 4 and creates no task
    Given I note the counts of tasks, leases and events
    When I run the client 'vh new zz "Lost" --start "x"' as actor "codex@laptop"
    Then the exit code is 4
    And stdout is empty
    And stderr is one line ending with "→ vh init ls"
    And the counts of tasks, leases and events changed by 0, 0 and 0

  Scenario Outline: a malformed command line exits 2 with one usage line and sends nothing
    Given I note the counts of tasks, leases and events
    When I run the client '<command>' as actor "claude@laptop"
    Then the exit code is 2
    And stdout is empty
    And stderr is one line ending with '<line>'
    And the counts of tasks, leases and events changed by 0, 0 and 0

    Examples:
      | command                             | line                                                                                                 |
      | vh new                              | usage: vh new <initiative> "<title>" --start "<intent>" [--tag t]... [--res r]... [--ttl D] [--json] |
      | vh new aa "Title"                   | usage: vh new <initiative> "<title>" --start "<intent>" [--tag t]... [--res r]... [--ttl D] [--json] |
      | vh new aa                           | usage: vh new <initiative> "<title>" --start "<intent>" [--tag t]... [--res r]... [--ttl D] [--json] |
      | vh new aa "Title" --start           | usage: vh new <initiative> "<title>" --start "<intent>" [--tag t]... [--res r]... [--ttl D] [--json] |
      | vh new aa "Title" extra --start x   | usage: vh new <initiative> "<title>" --start "<intent>" [--tag t]... [--res r]... [--ttl D] [--json] |
      | vh new aa "Title" --start x --ttl 2 | invalid duration "2": want a whole number and a unit s, m, h or d, such as 90m                       |
