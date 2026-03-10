package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidebolt/plugin-frigate/pkg/frigate"
	runner "github.com/slidebolt/sdk-runner"
	"github.com/slidebolt/sdk-types"
)

// TestNilClientDiscovery exposes weakness: nil client causes panic or unexpected behavior
func TestNilClientDiscovery(t *testing.T) {
	p := NewPlugin()

	// Initialize without setting FRIGATE_URL - client will be nil
	p.OnInitialize(runner.Config{}, types.Storage{})

	// This should handle nil client gracefully, not panic
	_, err := p.discover()
	if err == nil {
		t.Fatal("expected error when client is nil, got nil")
	}

	// Error should be ErrOffline, not panic or other error
	if err != frigate.ErrOffline {
		t.Fatalf("expected ErrOffline, got: %v", err)
	}
}

// TestMQTTStateMemoryLeak exposes weakness: unbounded MQTT state growth
func TestMQTTStateMemoryLeak(t *testing.T) {
	p := NewPlugin()
	p.OnInitialize(runner.Config{}, types.Storage{})

	// Simulate many cameras sending many MQTT updates
	// In production, this could cause memory exhaustion
	for i := 0; i < 10000; i++ {
		cameraName := fmt.Sprintf("camera_%d", i)
		for j := 0; j < 100; j++ {
			p.updateMQTTState(cameraName, fmt.Sprintf("key_%d", j), fmt.Sprintf("value_%d", i*j))
		}
	}

	// Check that we can still access values (but warn about memory usage)
	val, ok := p.mqttValue("camera_5000", "key_50")
	if !ok {
		t.Fatal("expected to find MQTT value")
	}
	if val == "" {
		t.Fatal("expected non-empty MQTT value")
	}

	// In production, there should be cleanup logic for stale cameras
	t.Log("WARNING: MQTT state grows unbounded - potential memory leak with many cameras/updates")
}

// TestInvalidCameraNames exposes weakness: special characters in camera names can break entity IDs
func TestInvalidCameraNames(t *testing.T) {
	invalidNames := []string{
		"camera with spaces",
		"camera/with/slashes",
		"camera..dots",
		"camera@symbol",
		"camera#hash",
		"camera%percent",
		"",
		"   ",
		"camera\x00null",
		"camera\nnewline",
		"camera\ttab",
	}

	p := NewPlugin()
	p.OnInitialize(runner.Config{}, types.Storage{})

	for _, name := range invalidNames {
		deviceID := p.deviceID(name)
		entityID := p.streamEntityID(name, "main")

		// These should not panic and should produce valid entity IDs
		t.Logf("Camera name: %q -> deviceID: %q, entityID: %q", name, deviceID, entityID)

		// Check for potential issues
		if strings.Contains(entityID, " ") {
			t.Errorf("entity ID contains space for camera %q: %s", name, entityID)
		}
		if strings.Contains(entityID, "/") {
			t.Errorf("entity ID contains slash for camera %q: %s", name, entityID)
		}
	}
}

// TestEmptyURLHandling exposes weakness: empty or malformed URLs cause issues
func TestEmptyURLHandling(t *testing.T) {
	testCases := []struct {
		name     string
		frigate  string
		go2rtc   string
		expected string
	}{
		{"empty both", "", "", ""},
		{"empty frigate", "", "http://rtc.example", "http://rtc.example"},
		{"empty go2rtc", "http://frigate.example", "", "http://frigate.example"},
		{"trailing slash frigate", "http://frigate.example/", "", "http://frigate.example"},
		{"trailing slash go2rtc", "", "http://rtc.example/", "http://rtc.example"},
		{"with path", "http://frigate.example/api", "", "http://frigate.example/api"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPlugin()
			p.pConfig.FrigateURL = tc.frigate
			p.pConfig.Go2RTCURL = tc.go2rtc

			base := p.go2rtcBaseURL()
			t.Logf("Config: frigate=%q, go2rtc=%q -> base=%q", tc.frigate, tc.go2rtc, base)

			// Should not panic with empty URLs
			_ = p.go2rtcURLf("/stream.html?src=%s", "camera")
		})
	}
}

// TestTimestampOverflow exposes weakness: float64 timestamp overflow
func TestTimestampOverflow(t *testing.T) {
	p := NewPlugin()
	p.OnInitialize(runner.Config{}, types.Storage{})

	// Test extreme timestamp values that could overflow int64
	events := []frigate.FrigateEvent{
		{ID: "evt1", Camera: "cam1", Label: "person", StartTime: 1e308, EndTime: 1e308},   // Max float64
		{ID: "evt2", Camera: "cam1", Label: "person", StartTime: -1e308, EndTime: -1e308}, // Min float64
		{ID: "evt3", Camera: "cam1", Label: "person", StartTime: 0, EndTime: 0},
		{ID: "evt4", Camera: "cam1", Label: "person", StartTime: -1, EndTime: -1},
	}

	cam := &discoveredCamera{
		Name:       "cam1",
		LastEvents: make(map[string]frigate.FrigateEvent),
		AllEvents:  events,
	}

	// Build LastEvents map manually
	for _, evt := range events {
		if evt.Label != "" {
			if existing, ok := cam.LastEvents[evt.Label]; !ok || evt.StartTime > existing.StartTime {
				cam.LastEvents[evt.Label] = evt
			}
		}
	}

	// Should not panic with extreme timestamps
	state := p.frigateEventState(cam, "person")
	t.Logf("Event state with extreme timestamps: %+v", state)

	// Check timestamp conversion in tsFor function
	for _, evt := range events {
		result := tsFor(&evt)
		t.Logf("StartTime=%v -> tsFor=%q", evt.StartTime, result)
	}
}

// TestConcurrentAccess exposes race conditions in the plugin
func TestConcurrentAccess(t *testing.T) {
	p := NewPlugin()
	p.OnInitialize(runner.Config{}, types.Storage{})

	var wg sync.WaitGroup

	// Concurrent reads and writes to MQTT state
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			camera := fmt.Sprintf("cam%d", id%10)
			p.updateMQTTState(camera, "motion", "on")
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			camera := fmt.Sprintf("cam%d", id%10)
			_, _ = p.mqttValue(camera, "motion")
		}(i)
	}

	// Test with timeout to detect deadlocks
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Log("Concurrent access completed without deadlock")
	case <-time.After(5 * time.Second):
		t.Fatal("Concurrent access test timed out - possible deadlock")
	}
}

// TestEventSinkNilPointer exposes weakness: nil EventSink causes silent failures
func TestEventSinkNilPointer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"camera_fps":15.0}}}`)
		case "/api/streams":
			fmt.Fprintln(w, `{"cam1":{"producers":[]}}`)
		case "/api/events":
			fmt.Fprintln(w, `[]`)
		}
	}))
	defer ts.Close()

	p := NewPlugin()

	// Initialize with nil EventSink
	p.OnInitialize(runner.Config{EventSink: nil}, types.Storage{})
	_ = os.Setenv("FRIGATE_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	p.OnInitialize(runner.Config{EventSink: nil}, types.Storage{})

	// This should not panic with nil EventSink
	cam := &discoveredCamera{
		Name:   "cam1",
		Online: true,
	}

	// These emit events - should handle nil sink gracefully
	p.emitMainStreamEvent(cam)
	p.emitFrigateEventSensor(cam, "person")
}

// TestSanitizeCollisions exposes weakness: different inputs can produce same sanitized output
func TestSanitizeCollisions(t *testing.T) {
	p := NewPlugin()

	// These different camera names produce the same sanitized output
	collidingNames := [][]string{
		{"Camera One", "camera-one"},
		{"Camera  One", "camera--one"},
		{"CAMERA ONE", "camera-one"},
		{"camera one", "camera-one"},
		{"camera   one", "camera---one"},
		{"Front Door", "front-door"},
		{"front-door", "front-door"},
	}

	seen := make(map[string][]string)
	for _, pair := range collidingNames {
		input, expected := pair[0], pair[1]
		result := p.sanitize(input)
		if result != expected {
			t.Errorf("sanitize(%q) = %q, want %q", input, result, expected)
		}
		seen[result] = append(seen[result], input)
	}

	// Report potential collisions
	for sanitized, originals := range seen {
		if len(originals) > 1 {
			t.Logf("COLLISION: sanitized %q from: %v", sanitized, originals)
		}
	}
}

// TestEntityIDTooLong exposes weakness: very long camera names create unwieldy entity IDs
func TestEntityIDTooLong(t *testing.T) {
	p := NewPlugin()

	// Create a very long camera name
	longName := strings.Repeat("a", 1000)

	deviceID := p.deviceID(longName)
	entityID := p.streamEntityID(longName, "main")

	// Entity IDs can become extremely long
	t.Logf("Device ID length: %d", len(deviceID))
	t.Logf("Entity ID length: %d", len(entityID))

	if len(entityID) > 255 {
		t.Logf("WARNING: Entity ID exceeds 255 characters (length: %d)", len(entityID))
	}
}

// TestLabelCaseSensitivity exposes weakness: inconsistent label case handling
func TestLabelCaseSensitivity(t *testing.T) {
	p := NewPlugin()
	p.OnInitialize(runner.Config{}, types.Storage{})

	events := []frigate.FrigateEvent{
		{ID: "evt1", Camera: "cam1", Label: "Person", StartTime: 1000, EndTime: 0},
		{ID: "evt2", Camera: "cam1", Label: "person", StartTime: 2000, EndTime: 0},
		{ID: "evt3", Camera: "cam1", Label: "PERSON", StartTime: 3000, EndTime: 0},
	}

	lastEvents := make(map[string]frigate.FrigateEvent)
	for _, evt := range events {
		if existing, ok := lastEvents[evt.Label]; !ok || evt.StartTime > existing.StartTime {
			lastEvents[evt.Label] = evt
		}
	}

	cam := &discoveredCamera{
		Name:       "cam1",
		LastEvents: lastEvents,
		AllEvents:  events,
	}

	// Check how labels are stored (case sensitivity)
	t.Logf("LastEvents keys: ")
	for label := range cam.LastEvents {
		t.Logf("  Label: %q", label)
	}

	// Test trackedLabelSet - should be case insensitive
	cam.Config.Objects.Track = []string{"Person", "CAR", "dog"}
	tracked := p.trackedLabelSet(cam)
	t.Logf("Tracked labels: ")
	for label := range tracked {
		t.Logf("  Label: %q", label)
	}
}

// TestJSONMarshalErrors exposes weakness: JSON marshal errors are silently ignored
func TestJSONMarshalErrors(t *testing.T) {
	// Create an entity with a state that will fail to marshal
	// (Using circular references would fail, but Go doesn't easily support that)
	// Instead, test with valid state and verify it doesn't panic

	state := map[string]interface{}{
		"key":    "value",
		"number": 123,
		"bool":   true,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	t.Logf("Marshaled state: %s", string(data))

	// In the actual code, marshal errors are often ignored with _
	// This test documents that weakness
}

// TestMQTTTopicPrefixEmpty exposes weakness: empty MQTT topic prefix
func TestMQTTTopicPrefixEmpty(t *testing.T) {
	p := NewPlugin()

	// Set empty topic prefix - should use default
	p.OnInitialize(runner.Config{}, types.Storage{})

	// Check that empty prefix gets default value
	if p.pConfig.MQTTTopicPrefix == "" {
		t.Log("WARNING: Empty MQTT topic prefix not defaulted - could cause topic subscription issues")
	}
}

// TestPortZero exposes weakness: port 0 or invalid port values
func TestPortZero(t *testing.T) {
	testCases := []struct {
		input    string
		expected int
	}{
		{"0", 1883},   // Should default to 1883
		{"-1", 1883},  // Invalid, should default
		{"abc", 1883}, // Non-numeric, should default
		{"99999", 0},  // Out of range, current behavior accepts it
		{"65536", 0},  // Out of range
		{"", 1883},    // Empty, should default
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("port_%s", tc.input), func(t *testing.T) {
			_ = os.Setenv("FRIGATE_MQTT_PORT", tc.input)
			defer os.Unsetenv("FRIGATE_MQTT_PORT")

			p := NewPlugin()
			p.OnInitialize(runner.Config{}, types.Storage{})

			t.Logf("Port input: %q -> actual: %d (expected: %d)", tc.input, p.pConfig.MQTTPort, tc.expected)
		})
	}
}

// TestFrigateEventStateConsistency tests that event state is correctly computed
func TestFrigateEventStateConsistency(t *testing.T) {
	p := NewPlugin()
	p.OnInitialize(runner.Config{}, types.Storage{})

	// Create camera with specific event state
	cam := &discoveredCamera{
		Name: "test_cam",
		LastEvents: map[string]frigate.FrigateEvent{
			"person": {
				ID:          "evt123",
				Camera:      "test_cam",
				Label:       "person",
				StartTime:   1000.5,
				EndTime:     0, // Active event
				HasSnapshot: true,
				HasClip:     false,
			},
		},
	}

	state := p.frigateEventState(cam, "person")

	if !state.EventPresent {
		t.Error("EventPresent should be true")
	}
	if !state.HasSnapshot {
		t.Error("HasSnapshot should be true")
	}
	if state.HasClip {
		t.Error("HasClip should be false")
	}
	if state.LastEventID != "evt123" {
		t.Errorf("LastEventID = %q, want %q", state.LastEventID, "evt123")
	}

	// Test non-existent label
	emptyState := p.frigateEventState(cam, "car")
	if emptyState.EventPresent {
		t.Error("EventPresent should be false for non-existent label")
	}
}

// TestAvailabilitySystemEntityCreation tests availability entity for system device
func TestAvailabilitySystemEntityCreation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"camera_fps":15.0}}}`)
		case "/api/streams":
			fmt.Fprintln(w, `{"cam1":{"producers":[]}}`)
		case "/api/events":
			fmt.Fprintln(w, `[]`)
		}
	}))
	defer ts.Close()

	testCases := []struct {
		name          string
		frigateURL    string
		wantAvailable bool
	}{
		{"with client", ts.URL, true},
		{"without client", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPlugin()

			if tc.frigateURL != "" {
				_ = os.Setenv("FRIGATE_URL", tc.frigateURL)
				defer os.Unsetenv("FRIGATE_URL")
			}

			p.OnInitialize(runner.Config{}, types.Storage{})

			entities, err := p.OnEntityDiscover("frigate-system", nil)
			if err != nil {
				t.Fatalf("OnEntityDiscover failed: %v", err)
			}

			// Look for availability entity
			var found bool
			for _, ent := range entities {
				if ent.ID == "availability" {
					found = true
					var state availabilityState
					if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
						if state.Available != tc.wantAvailable {
							t.Errorf("availability = %v, want %v", state.Available, tc.wantAvailable)
						}
					}
				}
			}

			if tc.frigateURL != "" && !found {
				t.Error("availability entity not found")
			}
		})
	}
}

// TestDiscoveryWithFailingEndpoints tests behavior when some endpoints fail
func TestDiscoveryWithFailingEndpoints(t *testing.T) {
	failCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			// Config always works
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			// Stats fails sometimes
			failCount++
			if failCount%2 == 0 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprintln(w, `{"cameras":{"cam1":{"camera_fps":15.0}}}`)
		case "/api/streams":
			// Streams fails
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/api/events":
			// Events returns empty
			fmt.Fprintln(w, `[]`)
		}
	}))
	defer ts.Close()

	p := NewPlugin()
	_ = os.Setenv("FRIGATE_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	p.OnInitialize(runner.Config{}, types.Storage{})

	// Discovery should still work even with some failing endpoints
	cameras, err := p.discover()
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}

	if len(cameras) == 0 {
		t.Fatal("expected at least one camera")
	}

	// Camera should still be discovered even without stats/streams
	cam := cameras[0]
	if cam.Name != "cam1" {
		t.Errorf("camera name = %q, want cam1", cam.Name)
	}
}

// TestBoolOnVariations tests the boolOn function with various inputs
func TestBoolOnVariations(t *testing.T) {
	testCases := []struct {
		input    string
		expected bool
	}{
		{"on", true},
		{"ON", true},
		{"On", true},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"1", true},
		{"off", false},
		{"false", false},
		{"0", false},
		{"", false},
		{"  ", false},
		{"yes", false},
		{"no", false},
		{"maybe", false},
	}

	for _, tc := range testCases {
		result := boolOn(tc.input)
		if result != tc.expected {
			t.Errorf("boolOn(%q) = %v, want %v", tc.input, result, tc.expected)
		}
	}
}

// TestHealthCheckWithoutClient tests health check when client is not configured
func TestHealthCheckWithoutClient(t *testing.T) {
	p := NewPlugin()
	p.OnInitialize(runner.Config{}, types.Storage{})

	_, err := p.OnHealthCheck()
	if err == nil {
		t.Error("expected error when client not configured")
	}
}

// TestEventAggregation tests event aggregation logic
func TestEventAggregation(t *testing.T) {
	p := NewPlugin()

	events := []frigate.FrigateEvent{
		{ID: "evt1", Camera: "cam1", Label: "person", StartTime: 1000, EndTime: 0},  // Active
		{ID: "evt2", Camera: "cam1", Label: "person", StartTime: 900, EndTime: 950}, // Ended
		{ID: "evt3", Camera: "cam1", Label: "person", StartTime: 800, EndTime: 850}, // Ended
		{ID: "evt4", Camera: "cam1", Label: "car", StartTime: 1100, EndTime: 0},     // Active, different label
		{ID: "evt5", Camera: "cam1", Label: "car", StartTime: 500, EndTime: 600},    // Ended
		{ID: "evt6", Camera: "cam1", Label: "", StartTime: 700, EndTime: 0},         // Empty label
	}

	cam := &discoveredCamera{
		Name:      "cam1",
		AllEvents: events,
	}

	agg := p.eventAggByLabel(cam)

	// Check person aggregation
	if personAgg, ok := agg["person"]; ok {
		if personAgg.Count != 3 {
			t.Errorf("person count = %d, want 3", personAgg.Count)
		}
		if personAgg.ActiveCount != 1 {
			t.Errorf("person active count = %d, want 1", personAgg.ActiveCount)
		}
		if personAgg.LastEvent == nil || personAgg.LastEvent.ID != "evt1" {
			t.Error("person last event mismatch")
		}
	} else {
		t.Error("person aggregation not found")
	}

	// Check car aggregation
	if carAgg, ok := agg["car"]; ok {
		if carAgg.Count != 2 {
			t.Errorf("car count = %d, want 2", carAgg.Count)
		}
		if carAgg.ActiveCount != 1 {
			t.Errorf("car active count = %d, want 1", carAgg.ActiveCount)
		}
	} else {
		t.Error("car aggregation not found")
	}

	// Empty label events should be ignored
	if _, ok := agg[""]; ok {
		t.Error("empty label should not be in aggregation")
	}
}
