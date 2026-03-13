Feature: Generic HTTP Request
  Scenario: Health Check with Generic Steps
    Given I send a GET request to "/_internal/health?id={plugin_id}"
    Then the response status code should be 200
