Feature: GET /api/v1/attention/now, the data behind the Now view
  One read answers the attention sections, the tasks with a live lease each with its live leases nested, and the tasks
  without one whose last event is inside window (8h, 24h or 3d), each with that last event. The check-in at
  GET /api/v1/now is a different read and keeps its meaning. The tag and initiative filters narrow all three parts.
  Event times are backdated through the database, never waited out, and the sweeper is off so a lease that ran out is
  expired by the read itself.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port with BOARD_SWEEP_INTERVAL=off
    And I create the initiative "aa" titled "First" with tags "web" as actor "claude@laptop"
    And I create the initiative "bb" titled "Second" with no tags as actor "claude@laptop"
    And I create a task in "aa" titled "one" as actor "claude@laptop"
    And I create a task in "aa" titled "two" as actor "claude@laptop"
    And I create a task in "bb" titled "three" as actor "claude@laptop"
    And I create a task in "bb" titled "four" as actor "claude@laptop"
    And every event on the board is 100 hours old

  Scenario: an untouched board answers three empty parts and the default window
    When I request the now board with "" as actor "operator@laptop"
    Then the response status is 200
    And the response path "window" is "24h"
    And the now attention counts are "0,0,0,0,0,0"
    And the response path "in_flight" has 0 items
    And the response path "recent" has 0 items

  Scenario: a live lease puts its task in flight with the lease nested
    When I start a lease with body "{'task':'aa-1','intent':'wire the thing'}" as actor "claude@laptop"
    And I request the now board with "" as actor "operator@laptop"
    Then the response status is 200
    And the tasks in "in_flight" are "aa-1"
    And the response path "in_flight.0.task.title" is "one"
    And the response path "in_flight.0.task.initiative" is "aa"
    And the response path "in_flight.0.task.status" is "open"
    And the response path "in_flight.0.leases" has 1 items
    And the response path "in_flight.0.leases.0.lease" is "L1"
    And the response path "in_flight.0.leases.0.holder.handle" is "claude@laptop"
    And the response path "in_flight.0.leases.0.intent" is "wire the thing"
    And the response path "in_flight.0.leases.0.expires_at" is present
    And the response path "recent" has 0 items

  Scenario: two live leases on one task are nested together and the task whose lease started last comes first
    When I start a lease with body "{'task':'aa-1','intent':'first'}" as actor "claude@laptop"
    And I start a lease with body "{'task':'bb-1','intent':'second'}" as actor "codex@laptop"
    And I start a lease with body "{'task':'aa-1','intent':'third'}" as actor "codex@laptop"
    And I request the now board with "" as actor "operator@laptop"
    Then the tasks in "in_flight" are "aa-1,bb-1"
    And the response path "in_flight.0.leases" has 2 items
    And the response path "in_flight.0.leases.0.lease" is "L1"
    And the response path "in_flight.0.leases.1.lease" is "L3"
    And the response path "in_flight.1.leases" has 1 items

  Scenario: an ended lease inside the window puts its task in recently touched and outside it does not
    When I start a lease with body "{'task':'aa-1','intent':'wire the thing'}" as actor "claude@laptop"
    And I end the lease "L1" with body "{'outcome':'wrapped up'}" as actor "claude@laptop"
    And the events of the task "aa-1" are 10 hours old
    And I request the now board with "window=24h" as actor "operator@laptop"
    Then the tasks in "recent" are "aa-1"
    And the response path "in_flight" has 0 items
    And the response path "recent.0.last_event.type" is "lease_ended"
    And the response path "recent.0.last_event.actor.handle" is "claude@laptop"
    And the response path "recent.0.task.title" is "one"
    When I request the now board with "window=8h" as actor "operator@laptop"
    Then the response path "window" is "8h"
    And the response path "recent" has 0 items
    When I request the now board with "window=3d" as actor "operator@laptop"
    Then the tasks in "recent" are "aa-1"
    When the events of the task "aa-1" are 30 hours old
    And I request the now board with "window=24h" as actor "operator@laptop"
    Then the response path "recent" has 0 items
    When I request the now board with "window=3d" as actor "operator@laptop"
    Then the tasks in "recent" are "aa-1"
    When the events of the task "aa-1" are 80 hours old
    And I request the now board with "window=3d" as actor "operator@laptop"
    Then the response path "recent" has 0 items

  Scenario: a task with a live lease is never also in recently touched
    When I start a lease with body "{'task':'aa-1','intent':'first'}" as actor "claude@laptop"
    And I end the lease "L1" with body "{'outcome':'pause'}" as actor "claude@laptop"
    And I start a lease with body "{'task':'aa-1','intent':'second'}" as actor "codex@laptop"
    And I request the now board with "" as actor "operator@laptop"
    Then the tasks in "in_flight" are "aa-1"
    And the response path "recent" has 0 items

  Scenario: a lease that ran out leaves flight and its task is recently touched
    When I start a lease with body "{'task':'aa-1','intent':'wire the thing'}" as actor "claude@laptop"
    And the lease "L1" ran out 10 seconds ago
    And I request the now board with "" as actor "operator@laptop"
    Then the response path "in_flight" has 0 items
    And the tasks in "recent" are "aa-1"
    And the response path "recent.0.last_event.type" is "lease_expired"
    And the now attention counts are "0,0,1,0,0,1"

  Scenario: the filters narrow the attention sections, the tasks in flight and the recently touched ones
    When I start a lease with body "{'task':'aa-1','intent':'one'}" as actor "claude:w1"
    And I start a lease with body "{'task':'bb-1','intent':'two'}" as actor "codex@laptop"
    And I start a lease with body "{'task':'aa-2','intent':'three'}" as actor "codex@laptop"
    And I end the lease "L3" with body "{'outcome':'x'}" as actor "codex@laptop"
    And I start a lease with body "{'task':'bb-2','intent':'four'}" as actor "codex@laptop"
    And I end the lease "L4" with body "{'outcome':'y'}" as actor "codex@laptop"
    And I raise an ask with body "{'task':'aa-1','question':'Which?','mode':'poll'}" as actor "claude@laptop"
    And I raise an ask with body "{'task':'bb-1','question':'Which one?','mode':'poll'}" as actor "claude@laptop"
    And I request the now board with "" as actor "operator@laptop"
    Then the tasks in "in_flight" are "bb-1,aa-1"
    And the tasks in "recent" are "bb-2,aa-2"
    And the now attention counts are "2,0,0,0,0,2"
    When I request the now board with "initiative=aa" as actor "operator@laptop"
    Then the tasks in "in_flight" are "aa-1"
    And the tasks in "recent" are "aa-2"
    And the now attention counts are "1,0,0,0,0,1"
    When I request the now board with "initiative=bb" as actor "operator@laptop"
    Then the tasks in "in_flight" are "bb-1"
    And the tasks in "recent" are "bb-2"
    And the now attention counts are "1,0,0,0,0,1"
    When I request the now board with "tag=web" as actor "operator@laptop"
    Then the tasks in "in_flight" are "aa-1"
    And the tasks in "recent" are "aa-2"
    And the now attention counts are "1,0,0,0,0,1"
    When I request the now board with "tag=claude" as actor "operator@laptop"
    Then the tasks in "in_flight" are "aa-1"
    And the response path "recent" has 0 items
    And the now attention counts are "1,0,0,0,0,1"
    When I request the now board with "initiative=bb&tag=web" as actor "operator@laptop"
    Then the response path "in_flight" has 0 items
    And the response path "recent" has 0 items
    And the now attention counts are "0,0,0,0,0,0"
    When I request the now board with "initiative=aa&initiative=bb" as actor "operator@laptop"
    Then the tasks in "in_flight" are "bb-1,aa-1"
    And the tasks in "recent" are "bb-2,aa-2"

  Scenario: an unknown initiative is 404 and a malformed tag is 400
    When I request the now board with "initiative=zz" as actor "operator@laptop"
    Then the response status is 404
    When I request the now board with "tag=bad tag" as actor "operator@laptop"
    Then the response status is 400

  Scenario: a bad window is 400 and changes nothing
    When I start a lease with body "{'task':'aa-1','intent':'wire the thing'}" as actor "claude@laptop"
    And I note the counts of asks, events and live leases
    And I note the lease rows
    And I request the now board with "window=2d" as actor "operator@laptop"
    Then the response status is 400
    And the counts of asks, events and live leases changed by 0, 0 and 0
    And the lease rows are unchanged
    When I request the now board with "window=" as actor "operator@laptop"
    Then the response status is 400
    When I request the now board with "window=24H" as actor "operator@laptop"
    Then the response status is 400
    And the counts of asks, events and live leases changed by 0, 0 and 0

  Scenario: the check-in at GET /api/v1/now keeps its meaning
    When I start a lease with body "{'task':'aa-1','intent':'wire the thing'}" as actor "claude@laptop"
    And I check in with "" as actor "claude@laptop"
    Then the response status is 200
    And the response members are exactly "actor,my_leases,others_leases,notices,conflicts,my_asks,notes,notes_more,attention"

  Scenario: every task the board names carries its own, initiative and actor-kind tags, and task_marks gives them by ref
    When I add the tag "mine" to the task "aa-1" as actor "claude@laptop"
    And I start a lease with body "{'task':'aa-1','intent':'tagged'}" as actor "claude:w1"
    And I start a lease with body "{'task':'bb-1','intent':'plain'}" as actor "codex:c1"
    And I end the lease "L2" without a body as actor "codex:c1"
    And I request the now board with "" as actor "operator@laptop"
    Then the response status is 200
    And the response path "in_flight.0.task.tags" is "mine"
    And the response path "in_flight.0.task.tag_marks" has 3 items
    And the response path "in_flight.0.task.tag_marks.0.tag" is "mine"
    And the response path "in_flight.0.task.tag_marks.0.source" is "task"
    And the response path "in_flight.0.task.tag_marks.1.tag" is "web"
    And the response path "in_flight.0.task.tag_marks.1.source" is "initiative"
    And the response path "in_flight.0.task.tag_marks.2.tag" is "claude"
    And the response path "in_flight.0.task.tag_marks.2.source" is "actor"
    And the response path "task_marks.aa-1.tag_marks" has 3 items
    And the response path "task_marks.aa-1.tags" is "mine"
    And the response path "recent.0.task.tags" has 0 items
    And the response path "recent.0.task.tag_marks" has 1 items
    And the response path "recent.0.task.tag_marks.0.source" is "actor"
    And the response path "recent.0.task.tag_marks.0.tag" is "codex"
    And the response path "task_marks.bb-1.tags" has 0 items
