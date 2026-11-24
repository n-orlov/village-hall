Feature: Asks and answers from the command line
  vh ask raises an ask and prints one ok line, vh ask show, ls and
  withdraw read and withdraw asks, and vh answer answers one, directly or
  as a relay of what a person said somewhere else. An ask that is not open
  exits 6 with the next action, an unknown ask exits 4.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I run the client 'vh init add aa "First one"' as actor "claude@laptop"
    And 3 tasks exist in the initiative "aa" as actor "claude@laptop"

  Scenario: ask prints one ok line and raises a stop ask
    When I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is exactly "ok ask A1 raised"
    And stderr is empty
    And exactly 1 ask_raised event exists for the ask "A1"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "mode" is "stop"
    And stdout JSON path "blocking" is "false"
    And stdout JSON path "task" is "aa-1"
    And stdout JSON path "asker" is "claude@laptop"

  Scenario: --stop is the default and is accepted
    When I run the client 'vh ask aa-1 "Which port?" --stop' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is exactly "ok ask A1 raised"

  Scenario: --details reads a file
    Given a text file holds "from the file"
    When I run the client 'vh ask aa-1 "Which port?" --details <file>' as actor "claude@laptop"
    Then stdout is exactly "ok ask A1 raised"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "details" is "from the file"

  Scenario: --details - reads standard input
    Given the standard input holds "from the pipe"
    When I run the client 'vh ask aa-1 "Which port?" --details -' as actor "claude@laptop"
    Then stdout is exactly "ok ask A1 raised"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "details" is "from the pipe"

  Scenario: a --details file that cannot be read is an error and raises nothing
    When I run the client 'vh ask aa-1 "Which port?" --details /nonexistent/details.txt' as actor "claude@laptop"
    Then the exit code is 1
    And stderr is exactly "cannot read the details file /nonexistent/details.txt"
    And there are 0 asks in the database

  Scenario: --option is repeatable and --blocking blocks the task
    When I run the client 'vh ask aa-1 "Which port?" --option 8080 --option 9090 --blocking' as actor "claude@laptop"
    Then stdout is exactly "ok ask A1 raised"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "options.0" is "8080"
    And stdout JSON path "options.1" is "9090"
    And stdout JSON path "blocking" is "true"
    When I run the client 'vh task show aa-1' as actor "claude@laptop"
    Then stdout line 1 matches "\(blocked\)"

  Scenario: ask show prints the line, the facts, the options and the details
    Given the standard input holds "needed for the proxy"
    And I run the client 'vh ask aa-2 "Which port?" --option 8080 --option 9090 --blocking --details -' as actor "claude@laptop"
    When I run the client 'vh ask show A1' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has 5 lines
    And stdout line 1 matches '^A1  open  aa-2  claude@laptop  "Which port\?"  \(blocking\)  asked just now$'
    And stdout line 2 matches "^initiative: aa$"
    And stdout line 3 matches "^option: 8080$"
    And stdout line 4 matches "^option: 9090$"
    And stdout line 5 matches "^needed for the proxy$"

  Scenario: ask show of an answered ask names the answer and of a withdrawn ask the reason
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    And I run the client 'vh ask aa-1 "Which host?"' as actor "claude@laptop"
    And I run the client 'vh answer A1 "8080"' as actor "nik@phone"
    And I run the client 'vh ask withdraw A2 "moot"' as actor "claude@laptop"
    When I run the client 'vh ask show A1' as actor "claude@laptop"
    Then stdout has 3 lines
    And stdout line 1 matches '^A1  answered  aa-1  claude@laptop  "Which port\?"  answered by nik@phone just now$'
    And stdout line 2 matches "^initiative: aa$"
    And stdout line 3 matches "^answer \(api\): 8080$"
    When I run the client 'vh ask show A2' as actor "claude@laptop"
    Then stdout has 3 lines
    And stdout line 1 matches '^A2  withdrawn  aa-1  claude@laptop  "Which host\?"  withdrawn just now$'
    And stdout line 3 matches "^reason: moot$"

  Scenario: ask ls lists the open asks, and filters by state, mine and task
    Given I run the client 'vh ask aa-1 "First?"' as actor "claude@laptop"
    And I run the client 'vh ask aa-2 "Second?" --blocking' as actor "other@laptop"
    And I run the client 'vh ask aa-2 "Third?"' as actor "claude@laptop"
    And I run the client 'vh answer A1 "yes"' as actor "nik@phone"
    When I run the client 'vh ask ls' as actor "claude@laptop"
    Then the exit code is 0
    And stdout has 2 lines
    And stdout line 1 matches '^A2  open  aa-2  other@laptop  "Second\?"  \(blocking\)  asked just now$'
    And stdout line 2 matches '^A3  open  aa-2  claude@laptop  "Third\?"  asked just now$'
    When I run the client 'vh ask ls --state answered' as actor "claude@laptop"
    Then stdout has 1 line
    And stdout line 1 matches '^A1  answered  aa-1  claude@laptop  "First\?"  answered by nik@phone just now$'
    When I run the client 'vh ask ls --state all' as actor "claude@laptop"
    Then stdout has 3 lines
    When I run the client 'vh ask ls --mine --state all' as actor "claude@laptop"
    Then stdout has 2 lines
    And stdout line 1 matches '^A1  '
    And stdout line 2 matches '^A3  '
    When I run the client 'vh ask ls --task aa-1 --state all' as actor "claude@laptop"
    Then stdout has 1 line
    And stdout line 1 matches '^A1  '
    When I run the client 'vh ask ls --task aa-3' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is empty

  Scenario: ask ls with a state outside the four is a usage error and an unknown task exits 4
    When I run the client 'vh ask ls --state pending' as actor "claude@laptop"
    Then the exit code is 2
    And stderr has 1 line
    When I run the client 'vh ask ls --task zz-1' as actor "claude@laptop"
    Then the exit code is 4
    And stderr is one line ending with "→ vh task ls"

  Scenario: ask withdraw with a reason prints one ok line
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    When I run the client 'vh ask withdraw A1 "no longer needed"' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is exactly "ok ask A1 withdrawn"
    And the ask "A1" is "withdrawn" in the database
    And exactly 1 ask_withdrawn event exists for the ask "A1"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "withdraw_reason" is "no longer needed"

  Scenario: ask withdraw without a reason
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    When I run the client 'vh ask withdraw a1' as actor "claude@laptop"
    Then stdout is exactly "ok ask A1 withdrawn"

  Scenario: answer prints one ok line
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    When I run the client 'vh answer A1 "8080"' as actor "nik@phone"
    Then the exit code is 0
    And stdout is exactly "ok ask A1 answered"
    And the ask "A1" is "answered" in the database
    And exactly 1 ask_answered event exists for the ask "A1"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "answer" is "8080"
    And stdout JSON path "answered_by" is "nik@phone"
    And stdout JSON path "channel" is "api"

  Scenario: answer --relay --for names the person the answer came from
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    When I run the client 'vh answer A1 "8080" --relay --for nik@phone' as actor "relay@bridge"
    Then the exit code is 0
    And stdout is exactly "ok ask A1 answered"
    And the latest ask_answered event of the ask "A1" is on behalf of "nik@phone"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "channel" is "relay"
    And stdout JSON path "on_behalf_of" is "nik@phone"
    And stdout JSON path "answered_by" is "relay@bridge"
    When I run the client 'vh ask show A1' as actor "claude@laptop"
    Then stdout line 3 matches "^answer \(relay for nik@phone\): 8080$"

  Scenario: answer --relay without --for is a relay with no named person
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    When I run the client 'vh answer A1 "8080" --relay' as actor "relay@bridge"
    Then stdout is exactly "ok ask A1 answered"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "channel" is "relay"

  Scenario: --for without --relay is a usage error and answers nothing
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    When I run the client 'vh answer A1 "8080" --for nik@phone' as actor "relay@bridge"
    Then the exit code is 2
    And stderr has 1 line
    And the ask "A1" is "open" in the database

  Scenario: --json prints the object on each of the six commands
    When I run the client 'vh ask aa-1 "Which port?" --json' as actor "claude@laptop"
    Then stdout is a JSON object with a ULID id
    And stdout JSON path "ref" is "A1"
    And stdout JSON path "state" is "open"
    And I run the client 'vh ask aa-1 "Which host?" --json' as actor "claude@laptop"
    And I run the client 'vh ask aa-1 "Which user?" --json' as actor "claude@laptop"
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout is a JSON object with a ULID id
    And stdout JSON path "question" is "Which port?"
    When I run the client 'vh ask ls --json' as actor "claude@laptop"
    Then stdout is a JSON array of 3 objects with ULID ids
    When I run the client 'vh ask withdraw A1 "moot" --json' as actor "claude@laptop"
    Then stdout JSON path "ref" is "A1"
    And stdout JSON path "state" is "withdrawn"
    And stdout JSON path "withdraw_reason" is "moot"
    When I run the client 'vh answer A2 "root" --json' as actor "nik@phone"
    Then stdout JSON path "ref" is "A2"
    And stdout JSON path "state" is "answered"
    And stdout JSON path "channel" is "api"
    When I run the client 'vh answer A3 "alice" --relay --for nik@phone --json' as actor "relay@bridge"
    Then stdout JSON path "ref" is "A3"
    And stdout JSON path "state" is "answered"
    And stdout JSON path "channel" is "relay"
    And stdout JSON path "on_behalf_of" is "nik@phone"

  Scenario: answer and withdraw of an ask that is not open exit 6 with the next action
    Given I run the client 'vh ask aa-1 "Which port?"' as actor "claude@laptop"
    And I run the client 'vh ask aa-1 "Which host?"' as actor "claude@laptop"
    And I run the client 'vh answer A1 "8080"' as actor "nik@phone"
    And I run the client 'vh ask withdraw A2' as actor "claude@laptop"
    When I run the client 'vh answer A1 "9090"' as actor "nik@phone"
    Then the exit code is 6
    And stdout is empty
    And stderr is one line ending with "→ vh ask show A1"
    When I run the client 'vh answer A2 "9090"' as actor "nik@phone"
    Then the exit code is 6
    And stderr is one line ending with "→ vh ask show A2"
    When I run the client 'vh ask withdraw A1' as actor "claude@laptop"
    Then the exit code is 6
    And stderr is one line ending with "→ vh ask show A1"
    When I run the client 'vh ask withdraw A2 "again" --json' as actor "claude@laptop"
    Then the exit code is 6
    And stdout is empty
    And stderr is one line ending with "→ vh ask show A2"
    And exactly 1 ask_answered event exists for the ask "A1"

  Scenario Outline: an unknown ask exits 4 with the next action
    When I run the client '<command>' as actor "claude@laptop"
    Then the exit code is 4
    And stdout is empty
    And stderr is one line ending with "→ vh ask ls"

    Examples:
      | command                    |
      | vh ask show A99         |
      | vh ask show A99 --json  |
      | vh ask withdraw A99     |
      | vh answer A99 "yes"     |
      | vh answer A99 "yes" --relay --for nik@phone |

  Scenario: ask on an unknown task exits 4 and on a closed task exits 6
    When I run the client 'vh ask zz-1 "Which port?"' as actor "claude@laptop"
    Then the exit code is 4
    And stderr is one line ending with "→ vh task ls"
    Given I run the client 'vh task close aa-3' as actor "claude@laptop"
    When I run the client 'vh ask aa-3 "Which port?"' as actor "claude@laptop"
    Then the exit code is 6
    And stderr is one line ending with "→ vh task show aa-3"
    And there are 0 asks in the database

  Scenario: a question the server refuses exits 1 with its message
    When I run the client 'vh ask aa-1 ""' as actor "claude@laptop"
    Then the exit code is 1
    And stderr has 1 line
    And there are 0 asks in the database

  Scenario: the ask flags come before or after the positionals
    When I run the client 'vh ask --blocking --option x aa-1 "Before?"' as actor "claude@laptop"
    Then stdout is exactly "ok ask A1 raised"
    When I run the client 'vh ask aa-1 "After?" --blocking --option=y --json' as actor "claude@laptop"
    Then stdout JSON path "ref" is "A2"
    And stdout JSON path "blocking" is "true"
    And stdout JSON path "options.0" is "y"
    When I run the client 'vh ask show --json A1' as actor "claude@laptop"
    Then stdout JSON path "options.0" is "x"
    And stdout JSON path "blocking" is "true"
    When I run the client 'vh ask ls --state all --mine' as actor "claude@laptop"
    Then stdout has 2 lines

  Scenario: the answer flags come before or after the positionals
    Given I run the client 'vh ask aa-1 "One?"' as actor "claude@laptop"
    And I run the client 'vh ask aa-1 "Two?"' as actor "claude@laptop"
    When I run the client 'vh answer --relay --for nik@phone A1 "before"' as actor "relay@bridge"
    Then stdout is exactly "ok ask A1 answered"
    When I run the client 'vh answer A2 "after" --for=nik@phone --relay --json' as actor "relay@bridge"
    Then stdout JSON path "channel" is "relay"
    And stdout JSON path "on_behalf_of" is "nik@phone"
    And stdout JSON path "answer" is "after"

  Scenario: a wrong argument count or an unknown flag is exit 2 with the usage line
    When I run the client 'vh ask aa-1' as actor "claude@laptop"
    Then the exit code is 2
    And stderr has 1 line
    And stderr is one line ending with "[--json]"
    When I run the client 'vh ask aa-1 "q" --bogus' as actor "claude@laptop"
    Then the exit code is 2
    When I run the client 'vh ask show' as actor "claude@laptop"
    Then the exit code is 2
    When I run the client 'vh ask withdraw' as actor "claude@laptop"
    Then the exit code is 2
    When I run the client 'vh answer A1' as actor "claude@laptop"
    Then the exit code is 2
    And stderr is one line ending with "[--relay] [--for <handle>] [--json]"
    And there are 0 asks in the database

  Scenario: ask --poll prints the answer given while it waits and exits 0
    When I start the client 'vh ask aa-1 "Which port?" --poll 30s' as actor "claude@laptop" in the background
    And within 10 seconds the poll of the ask "A1" is seen in the database
    Then the background client is still waiting
    When I run the client 'vh answer A1 "8080"' as actor "nik@phone"
    Then the exit code is 0
    When the background client ends within 10 seconds
    Then the exit code is 0
    And stdout is exactly "ok A1 answered by nik@phone: 8080"
    And stderr is empty
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "mode" is "poll"
    And stdout JSON path "state" is "answered"

  Scenario: ask --poll --json prints the ask that was answered
    When I start the client 'vh ask aa-1 "Which port?" --poll 30s --json' as actor "claude@laptop" in the background
    And within 10 seconds the poll of the ask "A1" is seen in the database
    And I run the client 'vh answer A1 "8080"' as actor "nik@phone"
    And the background client ends within 10 seconds
    Then the exit code is 0
    And stdout JSON path "state" is "answered"
    And stdout JSON path "answer" is "8080"

  Scenario: ask --poll prints the withdrawal and exits 6
    When I start the client 'vh ask aa-1 "Which port?" --poll 30s' as actor "claude@laptop" in the background
    And within 10 seconds the poll of the ask "A1" is seen in the database
    And I run the client 'vh ask withdraw A1 "moot"' as actor "claude@laptop"
    And the background client ends within 10 seconds
    Then the exit code is 6
    And stdout is exactly "A1 withdrawn: moot"
    And stderr is empty

  Scenario: ask --poll with a short deadline exits 3 and leaves the ask open
    When I run the client 'vh ask aa-1 "Which port?" --poll 1s' as actor "claude@laptop"
    Then the exit code is 3
    And stdout is exactly "A1 still open → vh ask show A1"
    And stderr is empty
    And the ask "A1" is "open" in the database
    When I run the client 'vh ask show A1 --json' as actor "claude@laptop"
    Then stdout JSON path "mode" is "poll"
    And stdout JSON path "state" is "open"

  Scenario: ask --poll on an unknown task exits 4 and raises nothing
    When I run the client 'vh ask zz-9 "Which port?" --poll 2s' as actor "claude@laptop"
    Then the exit code is 4
    And there are 0 asks in the database

  Scenario: a bad --poll value or --poll with --stop is a usage error
    When I run the client 'vh ask aa-1 "q" --poll soon' as actor "claude@laptop"
    Then the exit code is 2
    When I run the client 'vh ask aa-1 "q" --poll 5m --stop' as actor "claude@laptop"
    Then the exit code is 2
    And stderr is one line ending with "[--json]"
    And there are 0 asks in the database
