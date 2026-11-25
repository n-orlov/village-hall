Feature: GET /api/v1/now, the check-in
  The check-in answers the caller's actor, its live leases with task ref, title and expiry, the live leases of
  other actors, and the conflicts: other live leases that hold a resource of one of the caller's live leases or
  one of the resources asked about in ?resources=. It also answers my_asks (the asks the caller raised that are open,
  and those answered since its notes cursor or in the last 24 hours, with the answer), notes (after the caller's
  notes cursor, those on a task it holds a live lease on, on a resource of its live leases, mentioning it, or
  broadcasts: at most 20 summaries and notes_more) and attention (the counts). Reading it moves no cursor.
  Expiry is made by backdating the row with the sweeper off, never by waiting out a ttl.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port with BOARD_SWEEP_INTERVAL=off
    And I create the initiative "aa" titled "First" with no tags as actor "claude@laptop"
    And I create a task in "aa" titled "wire the thing" as actor "claude@laptop"
    And I create a task in "aa" titled "test the thing" as actor "claude@laptop"

  Scenario: with nothing on the board every list is empty, the counts are zero and all nine members are there
    When I check in with "" as actor "claude@laptop"
    Then the response status is 200
    And the response members are exactly "actor,my_leases,others_leases,notices,conflicts,my_asks,notes,notes_more,attention"
    And the response path "my_asks" has 0 items
    And the response path "notes" has 0 items
    And the response path "notes_more" is "0"
    And the check-in attention counts are "0,0,0,0,0,0"
    And the response path "actor" is "claude@laptop"
    And the response path "my_leases" has 0 items
    And the response path "others_leases" has 0 items
    And the response path "notices" has 0 items
    And the response path "conflicts" has 0 items

  Scenario: my live lease is listed with its task ref, title and expiry
    Given I start a lease with body "{'task':'aa-1','intent':'wiring','resources':['repo:x']}" as actor "claude@laptop"
    When I check in with "" as actor "claude@laptop"
    Then the response status is 200
    And the response members are exactly "actor,my_leases,others_leases,notices,conflicts,my_asks,notes,notes_more,attention"
    And the response path "my_leases" lists the refs "L1"
    And the response path "my_leases.0.task" is "aa-1"
    And the response path "my_leases.0.title" is "wire the thing"
    And the response path "my_leases.0.intent" is "wiring"
    And the response path "my_leases.0.expires_at" is the expiry stored for the lease "L1"
    And the response path "others_leases" has 0 items
    And the response path "conflicts" has 0 items

  Scenario: a lease of another actor is listed under others only
    Given I start a lease with body "{'task':'aa-2','intent':'testing','resources':['repo:y']}" as actor "codex@laptop"
    When I check in with "" as actor "claude@laptop"
    Then the response path "my_leases" has 0 items
    And the response path "others_leases" lists the refs "L1"
    And the response path "others_leases.0.actor" is "codex@laptop"
    And the response path "others_leases.0.task" is "aa-2"
    And the response path "conflicts" has 0 items
    When I check in with "" as actor "codex@laptop"
    Then the response path "my_leases" lists the refs "L1"
    And the response path "my_leases.0.title" is "test the thing"
    And the response path "others_leases" has 0 items

  Scenario: another actor holding a resource of my lease is a conflict
    Given I start a lease with body "{'task':'aa-1','intent':'wiring','resources':['repo:x','db:main']}" as actor "claude@laptop"
    And I start a lease with body "{'task':'aa-2','intent':'testing','resources':['db:main','host:ci']}" as actor "codex@laptop"
    When I check in with "" as actor "claude@laptop"
    Then the response path "my_leases" lists the refs "L1"
    And the response path "others_leases" lists the refs "L2"
    And the response path "conflicts" has 1 items
    And the response path "conflicts.0.lease" is "L2"
    And the response path "conflicts.0.holder" is "codex@laptop"
    And the response path "conflicts.0.task" is "aa-2"
    And the response path "conflicts.0.intent" is "testing"
    And the response path "conflicts.0.resources" is "db:main"
    And the response path "conflicts.0.expires_at" is the expiry stored for the lease "L2"

  Scenario: a resource asked about conflicts with a live lease of another
    Given I start a lease with body "{'task':'aa-2','intent':'testing','resources':['db:main','host:ci']}" as actor "codex@laptop"
    When I check in with "resources=host:ci,repo:free" as actor "claude@laptop"
    Then the response status is 200
    And the response path "my_leases" has 0 items
    And the response path "conflicts.0.lease" is "L1"
    And the response path "conflicts.0.resources" is "host:ci"
    When I check in with "resources=repo:free" as actor "claude@laptop"
    Then the response path "conflicts" has 0 items
    And the response path "others_leases" lists the refs "L1"

  Scenario: my own lease does not conflict with the resources I ask about
    Given I start a lease with body "{'task':'aa-1','intent':'wiring','resources':['repo:x']}" as actor "claude@laptop"
    When I check in with "resources=repo:x" as actor "claude@laptop"
    Then the response path "my_leases" lists the refs "L1"
    And the response path "conflicts" has 0 items

  Scenario: a lease past its expiry is left out and expired once, with the sweeper off
    Given I start a lease with body "{'task':'aa-1','intent':'wiring','resources':['repo:x']}" as actor "claude@laptop"
    And I start a lease with body "{'task':'aa-2','intent':'testing','resources':['repo:x']}" as actor "codex@laptop"
    And the lease "L1" ran out 30 seconds ago
    Then the lease "L1" is "live" in the database
    When I check in with "" as actor "claude@laptop"
    Then the response path "my_leases" has 0 items
    And the response path "others_leases" lists the refs "L2"
    And the response path "conflicts" has 0 items
    And the lease "L1" is "expired" in the database
    And exactly 1 lease_expired event exists for the lease "L1"
    When I check in with "" as actor "claude@laptop"
    Then exactly 1 lease_expired event exists for the lease "L1"

  Scenario: a malformed resources list is refused with a 400 and changes nothing
    When I note the counts of tasks, dependencies, events and actors
    And I check in with "resources=a,,b" as actor "claude@laptop"
    Then the response status is 400
    And the counts of tasks, dependencies, events and actors are unchanged

  Rule: My asks

    Scenario: my open asks are listed and an ask of another actor is not
      Given I raise an ask with body "{'task':'aa-1','question':'which branch?','options':['main','dev']}" as actor "claude@laptop"
      And I raise an ask with body "{'question':'not for claude'}" as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response path "my_asks" lists the refs "A1"
      And the response path "my_asks.0.question" is "which branch?"
      And the response path "my_asks.0.state" is "open"
      And the response path "my_asks.0.task" is "aa-1"
      And the response path "my_asks.0.answer" is absent
      When I check in with "" as actor "codex@laptop"
      Then the response path "my_asks" lists the refs "A2"
      And the check-in attention counts are "2,0,0,0,0,2"

    Scenario: an ask answered a moment ago is listed with its answer, and stays after my notes cursor moves
      Given I raise an ask with body "{'task':'aa-1','question':'which branch?'}" as actor "claude@laptop"
      And I answer the ask "A1" with body "{'answer':'main'}" as actor "operator@laptop"
      And I post a note with body "{'summary':'marker'}" as actor "codex@laptop"
      When I put the "notes" cursor with body "{'position':'N1'}" as actor "claude@laptop"
      And I check in with "" as actor "claude@laptop"
      Then the response path "my_asks" lists the refs "A1"
      And the response path "my_asks.0.state" is "answered"
      And the response path "my_asks.0.answer" is "main"
      And the response path "my_asks.0.answered_by" is "operator@laptop"
      And the check-in attention counts are "0,0,0,0,0,0"

    Scenario: a withdrawn ask is not listed
      Given I raise an ask with body "{'question':'never mind'}" as actor "claude@laptop"
      And I withdraw the ask "A1" with body "{'reason':'no longer'}" as actor "claude@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response path "my_asks" has 0 items

  Rule: Notes

    Scenario: only the notes that concern me are listed
      Given I start a lease with body "{'task':'aa-1','intent':'wiring','resources':['repo:x']}" as actor "claude@laptop"
      And I post a note with body "{'task':'aa-1','summary':'on my task'}" as actor "codex@laptop"
      And I post a note with body "{'task':'aa-2','summary':'on a task that is not mine'}" as actor "codex@laptop"
      And I post a note with body "{'task':'aa-2','resource':'repo:x','summary':'on my resource'}" as actor "codex@laptop"
      And I post a note with body "{'task':'aa-2','summary':'hey','mentions':['claude@laptop']}" as actor "codex@laptop"
      And I post a note with body "{'summary':'to everyone'}" as actor "codex@laptop"
      And I post a note with body "{'initiative':'aa','summary':'on the initiative only'}" as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response status is 200
      And the response path "notes" lists the refs "N1,N3,N4,N5"
      And the response path "notes.0.summary" is "on my task"
      And the response path "notes.0.has_body" is "false"
      And the response path "notes.0.body" is absent
      And the response path "notes_more" is "0"
      When I check in with "" as actor "codex@laptop"
      Then the response path "notes" lists the refs "N5"

    Scenario: a note on another actor's task that does not concern me is absent
      Given I post a note with body "{'task':'aa-2','summary':'not for claude'}" as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response path "notes" has 0 items
      And the response path "notes_more" is "0"

    Scenario: a mention is present, and a mention of someone else is not
      Given I post a note with body "{'task':'aa-2','summary':'for claude','mentions':['claude@laptop']}" as actor "codex@laptop"
      And I post a note with body "{'task':'aa-2','summary':'for someone else','mentions':['claude@laptop2']}" as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response path "notes" lists the refs "N1"

    Scenario: a broadcast is present, with or without a resource
      Given I post a note with body "{'summary':'everyone'}" as actor "codex@laptop"
      And I post a note with body "{'summary':'everyone about a host','resource':'host:z'}" as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response path "notes" lists the refs "N1,N2"

    Scenario: notes at or before my notes cursor are absent and reading the check-in moves no cursor
      Given I post a note with body "{'summary':'one'}" as actor "codex@laptop"
      And I post a note with body "{'summary':'two'}" as actor "codex@laptop"
      And I post a note with body "{'summary':'three'}" as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response path "notes" lists the refs "N1,N2,N3"
      And the "notes" cursor of "claude@laptop" is not set
      When I put the "notes" cursor with body "{'position':'N2'}" as actor "claude@laptop"
      And I check in with "" as actor "claude@laptop"
      Then the response path "notes" lists the refs "N3"
      And the "notes" cursor of "claude@laptop" holds the id of "N2"
      When I check in with "" as actor "claude@laptop"
      Then the response path "notes" lists the refs "N3"
      And the "notes" cursor of "claude@laptop" holds the id of "N2"

    Scenario: an acked list leaves nothing in the check-in
      Given I post a note with body "{'summary':'one'}" as actor "codex@laptop"
      And I post a note with body "{'summary':'two'}" as actor "codex@laptop"
      When I list the notes with "since=cursor&ack=true" as actor "claude@laptop"
      And I check in with "" as actor "claude@laptop"
      Then the response path "notes" has 0 items
      And the response path "notes_more" is "0"

    Scenario: at most 20 summaries are listed and notes_more counts the rest
      Given 23 broadcast notes are posted as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the response path "notes" has 20 items
      And the response path "notes.0.ref" is "N1"
      And the response path "notes.19.ref" is "N20"
      And the response path "notes_more" is "3"

  Rule: The attention counts

    Scenario: the counts are those of the attention view
      Given I start a lease with body "{'task':'aa-1','intent':'wiring'}" as actor "codex@laptop"
      And the lease "L1" ran out 30 seconds ago
      And I raise an ask with body "{'question':'q'}" as actor "codex@laptop"
      When I check in with "" as actor "claude@laptop"
      Then the check-in attention counts are "1,0,1,0,0,2"
      When I request the attention with "fields=counts" as actor "claude@laptop"
      Then the attention counts are "1,0,1,0,0,2"
