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
)

// TestNilClientDiscovery exposes weakness: nil client causes panic or unexpected behavior
func TestNilClientDiscovery(t *testing.T) {
	p := NewPlugin()

	// Initialize without setting FRIGATE_URL - client will be nil
	testInit(p)

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
	testInit(p)

	// Simulate many cameras sending many MQTT updates
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
	testInit(p)

	for _, name := range invalidNames {
		deviceID := p.deviceID(name)
		entityID := p.streamEntityID(name, "main")

		t.Logf("Camera name: %q -> deviceID: %q, entityID: %q", name, deviceID, entityID)

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
	testInit(p)

	events := []frigate.FrigateEvent{
		{ID: "evt1", Camera: "cam1", Label: "person", StartTime: 1e308, EndTime: 1e308},
		{ID: "evt2", Camera: "cam1", Label: "person", StartTime: -1e308, EndTime: -1e308},
		{ID: "evt3", Camera: "cam1", Label: "person", StartTime: 0, EndTime: 0},
		{ID: "evt4", Camera: "cam1", Label: "person", StartTime: -1, EndTime: -1},
	}

	cam := &discoveredCamera{
		Name:       "cam1",
		LastEvents: make(map[string]frigate.FrigateEvent),
		AllEvents:  events,
	}

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

	for _, evt := range events {
		result := tsFor(&evt)
		t.Logf("StartTime=%v -> tsFor=%q", evt.StartTime, result)
	}
}

// TestConcurrentAccess exposes race conditions in the plugin
func TestConcurrentAccess(t *testing.T) {
	p := NewPlugin()
	testInit(p)

	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			camera := fmt.Sprintf("cam%d", id%10)
			p.updateMQTTState(camera, "motion", "on")
		}(i)
	}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			camera := fmt.Sprintf("cam%d", id%10)
			_, _ = p.mqttValue(camera, "motion")
		}(i)
	}

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

	// Initialize with nil Events (default PluginContext has nil Events)
	testInit(p)
	_ = os.Setenv("FRIGATE_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	testInit(p)

	// These emit events - should handle nil sink gracefully
	cam := &discoveredCamera{
		Name:   "cam1",
		Online: true,
	}
	p.emitMainStreamEvent(cam)
	p.emitFrigateEventSensor(cam, "person")
}

// TestSanitizeCollisions exposes weakness: different inputs can produce same sanitized output
func TestSanitizeCollisions(t *testing.T) {
	p := NewPlugin()

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

	for sanitized, originals := range seen {
		if len(originals) > 1 {
			t.Logf("COLLISION: sanitized %q from: %v", sanitized, originals)
		}
	}
}

// TestEntityIDTooLong exposes weakness: very long camera names create unwieldy entity IDs
func TestEntityIDTooLong(t *testing.T) {
	p := NewPlugin()

	longName := strings.Repeat("a", 1000)

	deviceID := p.deviceID(longName)
	entityID := p.streamEntityID(longName, "main")

	t.Logf("Device ID length: %d", len(deviceID))
	t.Logf("Entity ID length: %d", len(entityID))

	if len(entityID) > 255 {
		t.Logf("WARNING: Entity ID exceeds 255 characters (length: %d)", len(entityID))
	}
}

// TestLabelCaseSensitivity exposes weakness: inconsistent label case handling
func TestLabelCaseSensitivity(t *testing.T) {
	p := NewPlugin()
	testInit(p)

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

	t.Logf("LastEvents keys: ")
	for label := range cam.LastEvents {
		t.Logf("  Label: %q", label)
	}

	cam.Config.Objects.Track = []string{"Person", "CAR", "dog"}
	tracked := p.trackedLabelSet(cam)
	t.Logf("Tracked labels: ")
	for label := range tracked {
		t.Logf("  Label: %q", label)
	}
}

// TestJSONMarshalErrors exposes weakness: JSON marshal errors are silently ignored
func TestJSONMarshalErrors(t *testing.T) {
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
}

// TestMQTTTopicPrefixEmpty exposes weakness: empty MQTT topic prefix
func TestMQTTTopicPrefixEmpty(t *testing.T) {
	p := NewPlugin()
	testInit(p)

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
		{"0", 1883},
		{"-1", 1883},
		{"abc", 1883},
		{"99999", 0},
		{"65536", 0},
		{"", 1883},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("port_%s", tc.input), func(t *testing.T) {
			_ = os.Setenv("FRIGATE_MQTT_PORT", tc.input)
			defer os.Unsetenv("FRIGATE_MQTT_PORT")

			p := NewPlugin()
			testInit(p)

			t.Logf("Port input: %q -> actual: %d (expected: %d)", tc.input, p.pConfig.MQTTPort, tc.expected)
		})
	}
}

// TestFrigateEventStateConsistency tests that event state is correctly computed
func TestFrigateEventStateConsistency(t *testing.T) {
	p := NewPlugin()
	testInit(p)

	cam := &discoveredCamera{
		Name: "test_cam",
		LastEvents: map[string]frigate.FrigateEvent{
			"person": {
				ID:          "evt123",
				Camera:      "test_cam",
				Label:       "person",
				StartTime:   1000.5,
				EndTime:     0,
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

			testInit(p)

			entities, err := p.entitiesForDevice("frigate-system")
			if err != nil {
				t.Fatalf("entitiesForDevice failed: %v", err)
			}

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
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			failCount++
			if failCount%2 == 0 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprintln(w, `{"cameras":{"cam1":{"camera_fps":15.0}}}`)
		case "/api/streams":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/api/events":
			fmt.Fprintln(w, `[]`)
		}
	}))
	defer ts.Close()

	p := NewPlugin()
	_ = os.Setenv("FRIGATE_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	testInit(p)

	cameras, err := p.discover()
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}

	if len(cameras) == 0 {
		t.Fatal("expected at least one camera")
	}

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

// TestHealthCheckWithoutClient tests that discover returns error when client is not configured
func TestHealthCheckWithoutClient(t *testing.T) {
	p := NewPlugin()
	testInit(p) // no FRIGATE_URL set → client is nil

	_, err := p.discover()
	if err == nil {
		t.Error("expected error when client not configured")
	}
}

// TestEventAggregation tests event aggregation logic
func TestEventAggregation(t *testing.T) {
	p := NewPlugin()

	events := []frigate.FrigateEvent{
		{ID: "evt1", Camera: "cam1", Label: "person", StartTime: 1000, EndTime: 0},
		{ID: "evt2", Camera: "cam1", Label: "person", StartTime: 900, EndTime: 950},
		{ID: "evt3", Camera: "cam1", Label: "person", StartTime: 800, EndTime: 850},
		{ID: "evt4", Camera: "cam1", Label: "car", StartTime: 1100, EndTime: 0},
		{ID: "evt5", Camera: "cam1", Label: "car", StartTime: 500, EndTime: 600},
		{ID: "evt6", Camera: "cam1", Label: "", StartTime: 700, EndTime: 0},
	}

	cam := &discoveredCamera{
		Name:      "cam1",
		AllEvents: events,
	}

	agg := p.eventAggByLabel(cam)

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

	if _, ok := agg[""]; ok {
		t.Error("empty label should not be in aggregation")
	}
}
