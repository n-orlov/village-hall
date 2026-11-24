Feature: Liveness and readiness
  GET /healthz answers while the process is up and is the only route that needs
  no credential. GET /readyz needs an API key (and a Board-Actor header) and
  then answers 200 only when the database is reachable and migrated, and 503
  otherwise. A key is made by `vh admin keys create` on a migrated database.

  Scenario: /healthz answers 200 without credentials
    Given the board server is running on a free port
    When I request GET "/healthz" without credentials
    Then the response status is 200

  Scenario: /readyz answers 401 without credentials
    Given the board server is running on a free port
    When I request GET "/readyz" without credentials
    Then the response status is 401

  Scenario: /readyz answers 200 with a key on a migrated store
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I request GET "/readyz" with the remembered key
    Then the response status is 200

  Scenario: /readyz answers 400 with a key and no Board-Actor header
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I request GET "/readyz" with the remembered key and no Board-Actor header
    Then the response status is 400

  @empty-database
  Scenario: /readyz answers 503 with a key on an unmigrated store with BOARD_MIGRATE=off
    Given I hold a key made on another database
    And the board server is running on a free port with BOARD_MIGRATE=off
    When I request GET "/readyz" with the remembered key
    Then the response status is 503

  @empty-database
  Scenario: /healthz still answers 200 on an unmigrated store
    Given the board server is running on a free port with BOARD_MIGRATE=off
    When I request GET "/healthz" without credentials
    Then the response status is 200

  @postgres @dedicated-database
  Scenario: /readyz answers 503 once the Postgres database is stopped
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port
    And I request GET "/readyz" with the remembered key
    Then the response status is 200
    When the database is stopped
    And I request GET "/readyz" with the remembered key
    Then the response status is 503

  @postgres @dedicated-database
  Scenario: /healthz still answers 200 once the Postgres database is stopped
    Given the board server is running on a free port
    When the database is stopped
    And I request GET "/healthz" without credentials
    Then the response status is 200
