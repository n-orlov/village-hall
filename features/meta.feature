Feature: What the server says about itself and the caller, GET /api/v1/meta
  A client asks the server which version and agent protocol version it runs and
  who it has authenticated, without touching the database.

  Background:
    Given a new database for the board commands
    When I run "vh admin migrate"
    And I run "vh admin keys create workstation"
    And I remember the key stdout printed
    And the board server is running on a free port

  Scenario: meta returns the server version, the protocol version, the principal and the actor
    When I run "vh version"
    And I request GET "/api/v1/meta" with the remembered key as actor "claude@laptop"
    Then the response status is 200
    And the response version is the version the last command printed
    And the response field "protocol" is "6"
    And the response field "principal" is "key:workstation"
    And the response field "actor" is "claude@laptop"
    When I request GET "/api/v1/meta" with the remembered key and no Board-Actor header
    Then the response status is 400
    When I request GET "/api/v1/meta" without credentials
    Then the response status is 401
