Feature: vh healthcheck probes the local server
  A container built FROM scratch has no shell and no curl, so the binary
  probes its own /healthz: exit 0 on a 200, exit 1 with one stderr line
  otherwise.

  Scenario: a running server is healthy
    Given the board server is running on a free port
    When I run "vh healthcheck" against the server's listen address
    Then the exit code is 0
    And stdout is exactly "ok healthz 200"
    And stderr is empty

  Scenario: no server is unhealthy
    When I run "vh healthcheck --url http://127.0.0.1:1/healthz"
    Then the exit code is 1
    And stdout is empty
    And stderr is exactly one line containing "vh: healthcheck: "

  Scenario: extra arguments are a usage error
    When I run "vh healthcheck now"
    Then the exit code is 2
    And stderr is exactly one line containing "usage: vh healthcheck [--url <url>]"
