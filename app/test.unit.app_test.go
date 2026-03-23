package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	frigateapp "github.com/slidebolt/plugin-frigate/app"
	domain "github.com/slidebolt/sb-domain"
	managersdk "github.com/slidebolt/sb-manager-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

func TestHelloManifest(t *testing.T) {
	got := frigateapp.New().Hello()
	if got.ID != frigateapp.PluginID {
		t.Fatalf("Hello().ID = %q, want %q", got.ID, frigateapp.PluginID)
	}
	if len(got.DependsOn) != 2 {
		t.Fatalf("Hello().DependsOn = %v, want messenger+storage", got.DependsOn)
	}
}

func TestDiscoveryCreatesCameraAndChildEntities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{
				"cameras": {
					"front_door": {
						"name": "front_door",
						"enabled": true,
						"detect": {"enabled": true},
						"motion": {"enabled": true},
						"record": {"enabled": false},
						"snapshots": {"enabled": true},
						"review": {"alerts": {"enabled": true}, "detections": {"enabled": false}},
						"objects": {"track": ["person", "dog"]},
						"zones": {"porch": {}, "driveway": {}}
					}
				}
			}`)
		case "/api/events":
			fmt.Fprintln(w, `[
				{
					"id": "evt-1",
					"label": "person",
					"camera": "front_door",
					"start_time": 1710000000,
					"has_clip": true,
					"has_snapshot": true,
					"data": {"type": "object", "score": 0.98}
				},
				{
					"id": "evt-2",
					"label": "dog",
					"camera": "front_door",
					"start_time": 1710000100,
					"end_time": 1710000200,
					"has_clip": false,
					"has_snapshot": true,
					"data": {"type": "object", "score": 0.88}
				}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)
	t.Setenv("FRIGATE_GO2RTC_URL", "https://go2rtc.example")
	t.Setenv("FRIGATE_EVENT_LIMIT", "20")

	env := managersdk.NewTestEnv(t)
	env.Start("messenger")
	env.Start("storage")

	app := frigateapp.New()
	if _, err := app.OnStart(map[string]json.RawMessage{
		"messenger": env.MessengerPayload(),
	}); err != nil {
		t.Fatalf("OnStart: %v", err)
	}
	defer app.OnShutdown()

	store := env.Storage()

	camera := getEntity(t, store, frigateapp.PluginID, "front_door", "front_door")
	cameraState, ok := camera.State.(frigateapp.CameraState)
	if !ok {
		t.Fatalf("camera state type = %T, want CameraState", camera.State)
	}
	if !cameraState.DetectEnabled || !cameraState.SnapshotsEnabled || cameraState.RecordEnabled {
		t.Fatalf("unexpected camera state: %+v", cameraState)
	}
	if cameraState.LastEvent == nil || cameraState.LastEvent.ID != "evt-2" {
		t.Fatalf("camera LastEvent = %+v, want evt-2", cameraState.LastEvent)
	}

	availability := getEntity(t, store, frigateapp.PluginID, "front_door", "availability")
	if availability.Type != "frigate_availability" {
		t.Fatalf("availability type = %q", availability.Type)
	}

	mainStream := getEntity(t, store, frigateapp.PluginID, "front_door", "stream-main")
	streamState, ok := mainStream.State.(frigateapp.StreamState)
	if !ok {
		t.Fatalf("stream state type = %T, want StreamState", mainStream.State)
	}
	if streamState.URL != "https://go2rtc.example/stream.html?src=front_door" {
		t.Fatalf("stream URL = %q", streamState.URL)
	}

	snapshot := getEntity(t, store, frigateapp.PluginID, "front_door", "image-latest")
	imageState, ok := snapshot.State.(frigateapp.ImageState)
	if !ok {
		t.Fatalf("image state type = %T, want ImageState", snapshot.State)
	}
	if imageState.URL != server.URL+"/api/front_door/latest.jpg" {
		t.Fatalf("snapshot URL = %q", imageState.URL)
	}

	personEvents := getEntity(t, store, frigateapp.PluginID, "front_door", "event-person")
	personState, ok := personEvents.State.(frigateapp.EventSensorState)
	if !ok {
		t.Fatalf("event state type = %T, want EventSensorState", personEvents.State)
	}
	if !personState.EventPresent || personState.LastEventID != "evt-1" {
		t.Fatalf("unexpected person event state: %+v", personState)
	}

	allCount := getEntity(t, store, frigateapp.PluginID, "front_door", "status-all-count")
	countState, ok := allCount.State.(frigateapp.StatusSensorState)
	if !ok {
		t.Fatalf("count state type = %T, want StatusSensorState", allCount.State)
	}
	if countState.Count != 2 {
		t.Fatalf("count = %d, want 2", countState.Count)
	}

	detect := getEntity(t, store, frigateapp.PluginID, "front_door", "status-detect")
	detectState, ok := detect.State.(frigateapp.StatusSensorState)
	if !ok {
		t.Fatalf("detect state type = %T, want StatusSensorState", detect.State)
	}
	if detectState.Value != "On" {
		t.Fatalf("detect value = %q, want On", detectState.Value)
	}

	entries, err := store.Query(storage.Query{Pattern: frigateapp.PluginID + ".front_door.>"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) < 10 {
		t.Fatalf("entity count = %d, want at least 10", len(entries))
	}
}

func TestDiscoveryReconcilesNewCameraWithoutRestart(t *testing.T) {
	var mu sync.RWMutex
	cameras := map[string]any{
		"front_door": map[string]any{
			"name":      "front_door",
			"enabled":   true,
			"detect":    map[string]any{"enabled": true},
			"motion":    map[string]any{"enabled": true},
			"record":    map[string]any{"enabled": true},
			"snapshots": map[string]any{"enabled": true},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			mu.RLock()
			payload := map[string]any{"cameras": cameras}
			mu.RUnlock()
			_ = json.NewEncoder(w).Encode(payload)
		case "/api/events":
			fmt.Fprintln(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)
	t.Setenv("FRIGATE_TIMEOUT_MS", "200")

	env := managersdk.NewTestEnv(t)
	env.Start("messenger")
	env.Start("storage")

	app := frigateapp.New()
	if _, err := app.OnStart(map[string]json.RawMessage{
		"messenger": env.MessengerPayload(),
	}); err != nil {
		t.Fatalf("OnStart: %v", err)
	}
	defer app.OnShutdown()

	store := env.Storage()
	if _, err := store.Get(domain.EntityKey{Plugin: frigateapp.PluginID, DeviceID: "front_door", ID: "front_door"}); err != nil {
		t.Fatalf("expected initial camera: %v", err)
	}

	mu.Lock()
	cameras["garage"] = map[string]any{
		"name":      "garage",
		"enabled":   true,
		"detect":    map[string]any{"enabled": true},
		"motion":    map[string]any{"enabled": true},
		"record":    map[string]any{"enabled": false},
		"snapshots": map[string]any{"enabled": true},
	}
	mu.Unlock()

	deadline := time.Now().Add(35 * time.Second)
	for {
		if _, err := store.Get(domain.EntityKey{Plugin: frigateapp.PluginID, DeviceID: "garage", ID: "garage"}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for reconciled garage camera")
		}
		time.Sleep(500 * time.Millisecond)
	}

	garage := getEntity(t, store, frigateapp.PluginID, "garage", "garage")
	if garage.Type != "frigate_camera" {
		t.Fatalf("garage type = %q", garage.Type)
	}
}

func getEntity(t *testing.T, store storage.Storage, plugin, deviceID, entityID string) domain.Entity {
	t.Helper()
	raw, err := store.Get(domain.EntityKey{Plugin: plugin, DeviceID: deviceID, ID: entityID})
	if err != nil {
		t.Fatalf("Get(%s.%s.%s): %v", plugin, deviceID, entityID, err)
	}
	var entity domain.Entity
	if err := json.Unmarshal(raw, &entity); err != nil {
		t.Fatalf("Unmarshal(%s.%s.%s): %v", plugin, deviceID, entityID, err)
	}
	return entity
}
