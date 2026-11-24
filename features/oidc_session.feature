Feature: Browser sessions: CSRF protection, logout and expiry
  A cookie-authenticated write must carry the session's CSRF token and come from
  the board's own origin. Logging out deletes the session row, so the old cookie
  is worth nothing. A session also ends when it has been idle too long and when
  its absolute lifetime is spent, whatever its activity; its row is removed by the
  next request presenting it that is not refused (a refused one changes no row),
  or by the sweeper at its absolute expiry. The expiry scenarios
  start the server with lifetimes of under a second, so the clock they drive is
  the server's own and no step sleeps for longer than five seconds.

  Scenario: a cookie write without the CSRF token is rejected
    Given the mock OIDC provider signs in "alice@example.com" with a verified email
    And the board server is running with OIDC login allowing "alice@example.com"
    When the browser opens "/"
    And the browser follows the redirects
    Then the response status is 200
    When the browser reads its CSRF token from "/auth/session"
    And the browser sends POST "/auth/logout" with no CSRF token and the board's own Origin
    Then the response status is 403
    And the response is a problem document saying "X-CSRF-Token"
    And the browser holds a session cookie
    And the database holds 1 session
    When the browser calls GET "/auth/session" as an API client
    Then the response status is 200

  Scenario: a cookie write with a foreign Origin is rejected
    Given the mock OIDC provider signs in "alice@example.com" with a verified email
    And the board server is running with OIDC login allowing "alice@example.com"
    When the browser opens "/"
    And the browser follows the redirects
    Then the response status is 200
    When the browser reads its CSRF token from "/auth/session"
    And the browser sends POST "/auth/logout" with the session's CSRF token and the Origin "https://evil.example"
    Then the response status is 403
    And the response is a problem document saying "Origin"
    When the browser sends POST "/auth/logout" with the session's CSRF token and no Origin
    Then the response status is 403
    And the database holds 1 session
    When the browser calls GET "/auth/session" as an API client
    Then the response status is 200

  Scenario: logout invalidates the session
    Given the mock OIDC provider signs in "alice@example.com" with a verified email
    And the board server is running with OIDC login allowing "alice@example.com"
    When the browser opens "/"
    And the browser follows the redirects
    Then the response status is 200
    When the browser reads its CSRF token from "/auth/session"
    And the browser remembers its session cookie
    And the browser sends POST "/auth/logout" with the session's CSRF token and the board's own Origin
    Then the response status is 204
    And the browser holds no session cookie
    And the database holds 0 sessions
    When the browser puts the remembered session cookie back
    And the browser calls GET "/auth/session" as an API client
    Then the response status is 401
    When the browser opens "/"
    Then the response redirects to "/auth/login"

  Scenario: a session idle for longer than the idle timeout expires
    Given the mock OIDC provider signs in "alice@example.com" with a verified email
    And the board server is running with OIDC login allowing "alice@example.com" and BOARD_SESSION_IDLE set to "900ms"
    When the browser opens "/"
    And the browser follows the redirects
    Then the response status is 200
    When the browser waits 0.45 seconds
    And the browser calls GET "/auth/session" as an API client
    Then the response status is 200
    When the browser waits 0.45 seconds
    And the browser calls GET "/auth/session" as an API client
    Then the response status is 200
    When the browser waits 1.35 seconds
    And the browser calls GET "/auth/session" as an API client
    Then the response status is 401
    And the database holds 1 session
    When the browser opens "/"
    Then the response redirects to "/auth/login"
    And the database holds 0 sessions

  Scenario: a session expires at its absolute lifetime however active it is
    Given the mock OIDC provider signs in "alice@example.com" with a verified email
    And the board server is running with OIDC login allowing "alice@example.com" and BOARD_SESSION_TTL set to "800ms"
    When the browser opens "/"
    And the browser follows the redirects
    Then the response status is 200
    When the browser waits 0.5 seconds
    And the browser calls GET "/auth/session" as an API client
    Then the response status is 200
    When the browser waits 0.5 seconds
    And the browser calls GET "/auth/session" as an API client
    Then the response status is 401
    And the database holds 1 session
    When the browser opens "/"
    Then the response redirects to "/auth/login"
    And the database holds 0 sessions
