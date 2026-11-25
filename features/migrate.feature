@empty-database
Feature: Migrating an empty store
  `vh admin migrate` builds the whole Phase 0 schema in an empty database,
  on SQLite and on Postgres alike, and a server started on the result is ready.
  Each scenario runs on the leg BOARD_DB_LEG names, and the same scenario must
  give the same answer on both.

  Scenario: migrating an empty store creates every Phase 0 table
    Given a new database for the board commands
    When I run "vh admin migrate"
    Then the exit code is 0
    And stdout is exactly "ok migrated, schema version 16"
    And the database holds every Phase 0 table

  Scenario: migrating twice leaves the store as one migration did
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin migrate"
    Then the exit code is 0
    And stdout is exactly "ok migrated, schema version 16"
    And the database holds every Phase 0 table

  Scenario: a server started on an empty store with BOARD_MIGRATE unset migrates it and is ready
    Given the board server is running on a free port
    When I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And I request GET "/readyz" with the remembered key
    Then the response status is 200
    And the database holds every Phase 0 table
