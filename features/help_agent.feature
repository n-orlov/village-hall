Feature: The agent protocol, vh help agent
  An agent reads the rules of working with the board from the binary itself:
  vh help agent prints a short cheat sheet headed with its version, records
  that the actor has read it, and prints even when no server can be reached.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port

  Scenario: every command and flag the text names exists in the CLI command tree
    When I run the client 'vh help agent' as actor "claude@laptop"
    Then the exit code is 0
    And stderr is empty
    And stdout line 1 matches "^vh protocol v6$"
    And the stdout text names at least 5 commands
    And every board command and flag the stdout text names exists in the command tree
    When I run the client 'vh help agent --version' as actor "claude@laptop"
    Then the exit code is 0
    And stdout is exactly "6"

  Scenario: protocol v6 is written around the shortcuts, keeps the v3 rules, asks for task tags and describes notices
    When I run the client 'vh help agent' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 1 matches "^vh protocol v6$"
    And the stdout text names the command 'vh start <task> "<intent>" --res host-a --ttl 2h'
    And the stdout text names the command 'vh new <slug> "<title>" --start "<intent>"'
    And the stdout text names the command 'vh ping "<progress>"'
    And the stdout text names the command 'vh finish "<outcome>" --done'
    And the stdout text names the command 'vh block <task> "<question>"'
    And the stdout text names the command 'vh catchup'
    And the stdout text names the command 'vh now --resources host-a'
    And the stdout text names the command 'vh task done <ref>'
    And the stdout text names the command 'vh task reopen <ref>'
    And the stdout text names the command 'vh lease ack L5'
    And the stdout text names the command 'vh task done --force'
    And the stdout text names the command 'vh task add'
    And stdout has the line "  Tag each task you create with what the work is about (`--tag t` on `vh new` and `vh task add`);"

  Scenario: protocol v6 describes notices: post, renew, end with an outcome, read notices at check-in
    When I run the client 'vh help agent' as actor "claude@laptop"
    Then the exit code is 0
    And stdout line 1 matches "^vh protocol v6$"
    And the stdout text names the command 'vh start <task> "<what, until>" --notice'
    And the stdout text names the command 'vh ping'
    And the stdout text names the command 'vh finish <task> "<outcome>"'
    And the stdout text names the command 'vh now'
    And stdout has the line "NOTICES: a broadcast that every agent and view sees until it ends or expires"
    And every board command and flag the stdout text names exists in the command tree

  Scenario: reading the text records protocol_seen through the actors endpoint
    Then there is no actor row for "claude@laptop"
    When I run the client 'vh help agent --version' as actor "claude@laptop"
    Then there is no actor row for "claude@laptop"
    When I run the client 'vh help agent' as actor "claude@laptop"
    Then the exit code is 0
    And the protocol_seen of the actor "claude@laptop" is 6
    When I record protocol version 7 as read as actor "claude@laptop" through the API
    Then the response status is 200
    And the response field "actor" is "claude@laptop"
    And the response field "protocol_seen" is "7"
    When I run the client 'vh help agent' as actor "claude@laptop"
    Then the protocol_seen of the actor "claude@laptop" is 7
    When I record protocol version -1 as read as actor "claude@laptop" through the API
    Then the response status is 400
    And the problem has a next field containing "version"

  Scenario: the text still prints when the server cannot be reached
    When I run the client 'vh help agent' as actor "claude@laptop" against the address "http://127.0.0.1:1"
    Then the exit code is 0
    And stderr is empty
    And stdout line 1 matches "^vh protocol v6$"
    And every board command and flag the stdout text names exists in the command tree
