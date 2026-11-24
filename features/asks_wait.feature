Feature: Ask long-poll
  GET /api/v1/asks/{ref}?wait=N (1 to 300 seconds, anything else is 400) returns at once when the ask is not
  open. Otherwise it holds the request until the ask is answered or withdrawn, which returns the new state, or
  until N seconds pass, which returns the still-open ask (200). The change feed wakes the wait. While a wait is
  held the ask's poll_seen_at is set, and asker_waiting is true when the ask is open, its mode is poll and
  poll_seen_at is within the last 60 seconds. The end of a wait writes poll_seen_at too, and time after it is
  simulated by moving the stored poll_seen_at back from that value, never by waiting. A wait that runs out is
  held for under a second: it asks for ?wait=1 with until (SPEC 7.2) at the instant the scenario names, and the
  server ends the hold there.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I create the initiative "aa" titled "First" with no tags as actor "claude@laptop"
    And I create a task in "aa" titled "one" as actor "claude@laptop"
    And I raise an ask with body "{'task':'aa-1','question':'Which database?','mode':'poll'}" as actor "claude@laptop"

  Scenario: a wait on an answered ask returns at once
    When I answer the ask "A1" with body "{'answer':'Postgres'}" as actor "operator@laptop"
    And I request the ask "A1" waiting "30" seconds as actor "claude@laptop"
    Then the response status is 200
    And the response arrived in under 3 seconds
    And the response path "state" is "answered"
    And the response path "answer" is "Postgres"
    And the response path "asker_waiting" is "false"
    And the poll of the ask "A1" is not seen in the database

  Scenario: a wait on a withdrawn ask returns at once
    When I withdraw the ask "A1" with body "{'reason':'moot'}" as actor "claude@laptop"
    And I request the ask "A1" waiting "30" seconds as actor "claude@laptop"
    Then the response status is 200
    And the response arrived in under 3 seconds
    And the response path "state" is "withdrawn"

  Scenario: an answer mid-wait returns it before the wait runs out
    When I start a wait of 60 seconds on the ask "A1" as actor "claude@laptop"
    Then within 5 seconds the poll of the ask "A1" is seen in the database
    And the wait is still held
    When I answer the ask "A1" with body "{'answer':'SQLite'}" as actor "operator@laptop"
    Then the wait ends within 5 seconds
    And the response status is 200
    And the response path "state" is "answered"
    And the response path "answer" is "SQLite"
    And the response path "asker_waiting" is "false"

  Scenario: a withdrawal mid-wait returns it
    When I start a wait of 60 seconds on the ask "A1" as actor "claude@laptop"
    Then within 5 seconds the poll of the ask "A1" is seen in the database
    When I withdraw the ask "A1" without a body as actor "claude@laptop"
    Then the wait ends within 5 seconds
    And the response status is 200
    And the response path "state" is "withdrawn"

  Scenario: a wait that runs out returns the open ask
    When I request the ask "A1" waiting until 0.3 seconds from now as actor "claude@laptop"
    Then the response status is 200
    And the response path "state" is "open"
    And the response path "answer" is absent
    And the response path "asker_waiting" is "true"
    And within 1 seconds the poll of the ask "A1" is seen in the database

  Scenario: asker_waiting is true during a wait and false 60 seconds after it ends
    When I start a wait on the ask "A1" until 0.9 seconds from now as actor "claude@laptop"
    Then within 0.9 seconds the poll of the ask "A1" is seen in the database
    When I request the ask "A1" as actor "operator@laptop"
    Then the response path "asker_waiting" is "true"
    And the wait is still held
    When the poll of the ask "A1" was last seen 50 seconds ago
    And I request the ask "A1" as actor "operator@laptop"
    Then the response path "asker_waiting" is "true"
    And the wait is still held
    Then the wait ends within 10 seconds
    And the response status is 200
    And the response path "state" is "open"
    And the response path "asker_waiting" is "true"
    And the poll of the ask "A1" was last seen when the wait ended
    When I request the ask "A1" as actor "operator@laptop"
    Then the response path "asker_waiting" is "true"
    When the clock is 55 seconds past the end of the wait on the ask "A1"
    And I request the ask "A1" as actor "operator@laptop"
    Then the response path "asker_waiting" is "true"
    When the clock is 61 seconds past the end of the wait on the ask "A1"
    And I request the ask "A1" as actor "operator@laptop"
    Then the response path "state" is "open"
    And the response path "asker_waiting" is "false"

  Scenario: a wait that makes the asker waiting appends one ask_polling event, and only that wait
    Then exactly 0 ask_polling events exist for the ask "A1"
    When I request the ask "A1" waiting until 0.3 seconds from now as actor "claude@laptop"
    Then exactly 1 ask_polling event exists for the ask "A1"
    And the latest ask_polling event of the ask "A1" is on the task "aa-1" and the initiative "aa"
    And the latest ask_polling event of the ask "A1" has principal "system:poll" and actor "claude@laptop"
    When I request the ask "A1" waiting until 0.3 seconds from now as actor "claude@laptop"
    Then exactly 1 ask_polling event exists for the ask "A1"
    When the poll of the ask "A1" was last seen 61 seconds ago
    And I request the ask "A1" waiting until 0.3 seconds from now as actor "claude@laptop"
    Then exactly 2 ask_polling events exist for the ask "A1"

  Scenario: an ask in stop mode is never waiting
    When I raise an ask with body "{'question':'Anything?'}" as actor "claude@laptop"
    And I request the ask "A2" waiting until 0.3 seconds from now as actor "claude@laptop"
    Then the response status is 200
    And the response path "state" is "open"
    And the response path "asker_waiting" is "false"

  Scenario Outline: a wait outside 1 to 300 seconds is refused
    When I request the ask "A1" waiting "<wait>" seconds as actor "claude@laptop"
    Then the response status is 400
    And the response path "next" is present
    And the poll of the ask "A1" is not seen in the database

    Examples:
      | wait |
      | 0    |
      | 301  |
      | -1   |
      | soon |

  Scenario: a wait on an ask that does not exist is 404
    When I request the ask "A9" waiting "5" seconds as actor "claude@laptop"
    Then the response status is 404
