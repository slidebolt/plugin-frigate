Feature: Frigate MQTT Event Handling
  # This feature verifies that the plugin correctly parses real-world
  # MQTT payloads captured from the Frigate event stream and updates
  # the SlideBolt entity states accordingly.

  Scenario: Parse a "new" car detection event
    Given a camera entity "plugin-frigate.camera_01.camera-state" named "Front Door"
    When I receive an MQTT message on "frigate/events" with payload from file "features/testdata/events/event_1.json"
    Then retrieving "plugin-frigate.camera_01.camera-state" succeeds
    And the camera "LastEvent" field should be updated
    And a SlideBolt notification event should be triggered

  Scenario: Parse an "end" event
    Given a camera entity "plugin-frigate.camera_01.camera-state" named "Front Door"
    When I receive an MQTT message on "frigate/events" with payload from file "features/testdata/events/event_6.json"
    Then retrieving "plugin-frigate.camera_01.camera-state" succeeds
