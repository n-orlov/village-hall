@empty-database
Feature: The store and vh admin migrate
  The binary embeds the goose migrations for the Phase 0 tables. `vh admin
  migrate` applies them, and a database whose schema is newer than the binary is
  refused instead of served.

  Scenario: vh admin migrate applies the embedded migrations to an empty database
    Given a new database for the board commands
    When I run "vh admin migrate"
    Then the exit code is 0
    And stdout is exactly "ok migrated, schema version 17"
    And stderr is empty

  Scenario: vh admin migrate is idempotent
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin migrate"
    Then the exit code is 0
    And stdout is exactly "ok migrated, schema version 17"

  Scenario: a database migrated by vh admin migrate is ready without BOARD_MIGRATE
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port with BOARD_MIGRATE=off
    And I request GET "/readyz" with the remembered key
    Then the response status is 200

  Scenario: vh admin migrate refuses a schema newer than the binary
    Given a new database for the board commands
    And I run "vh admin migrate"
    And the database records schema version 9999 as applied
    When I run "vh admin migrate"
    Then the exit code is 1
    And stdout is empty
    And stderr is exactly one line containing "newer than this binary"

  Scenario: vh serve exits non-zero with one stderr line on a schema newer than the binary
    Given a new database for the board commands
    And I run "vh admin migrate"
    And the database records schema version 9999 as applied
    When I run "vh serve"
    Then the exit code is 1
    And stdout is empty
    And stderr is exactly one line containing "newer than this binary"

  Scenario: vh serve refuses a newer schema even with BOARD_MIGRATE=off
    Given a new database for the board commands
    And I run "vh admin migrate"
    And the database records schema version 9999 as applied
    When I run "vh serve" with BOARD_MIGRATE=off
    Then the exit code is 1
    And stderr is exactly one line containing "newer than this binary"

  Scenario: vh admin rejects an unknown subcommand with the usage line
    When I run "vh admin frobnicate"
    Then the exit code is 2
    And stderr is exactly the usage line
