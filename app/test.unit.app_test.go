package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	frigateapp "github.com/slidebolt/plugin-frigate/app"
	domain "github.com/slidebolt/sb-domain"
	storage "github.com/slidebolt/sb-storage-sdk"
	testkit "github.com/slidebolt/sb-testkit"
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

func TestDiscoveryCreatesCameraScopedEntitiesWithoutRoot(t *testing.T) {
	var eventsCalls int
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
			eventsCalls++
			t.Fatalf("discovery must not call /api/events")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)
	t.Setenv("FRIGATE_GO2RTC_URL", "https://go2rtc.example")
	t.Setenv("FRIGATE_EVENT_LIMIT", "20")

	env := testkit.NewTestEnv(t)
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

	camera := getEntity(t, store, frigateapp.PluginID, "front_door", "camera-state")
	cameraState, ok := camera.State.(frigateapp.CameraState)
	if !ok {
		t.Fatalf("camera state type = %T, want CameraState", camera.State)
	}
	if !cameraState.DetectEnabled || !cameraState.SnapshotsEnabled || cameraState.RecordEnabled {
		t.Fatalf("unexpected camera state: %+v", cameraState)
	}
	if cameraState.LastEvent != nil {
		t.Fatalf("camera LastEvent = %+v, want nil until MQTT updates arrive", cameraState.LastEvent)
	}
	if _, err := store.Get(domain.EntityKey{Plugin: frigateapp.PluginID, DeviceID: "front_door", ID: "front_door"}); err == nil {
		t.Fatal("unexpected legacy root camera entity plugin-frigate.front_door.front_door")
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
	if personState.EventPresent || personState.LastEventID != "" {
		t.Fatalf("unexpected person event state before MQTT: %+v", personState)
	}

	allCount := getEntity(t, store, frigateapp.PluginID, "front_door", "status-all-count")
	countState, ok := allCount.State.(frigateapp.StatusSensorState)
	if !ok {
		t.Fatalf("count state type = %T, want StatusSensorState", allCount.State)
	}
	if countState.Count != 0 {
		t.Fatalf("count = %d, want 0 before MQTT", countState.Count)
	}

	detect := getEntity(t, store, frigateapp.PluginID, "front_door", "status-detect")
	detectState, ok := detect.State.(frigateapp.StatusSensorState)
	if !ok {
		t.Fatalf("detect state type = %T, want StatusSensorState", detect.State)
	}
	if detectState.Value != "On" {
		t.Fatalf("detect value = %q, want On", detectState.Value)
	}

	enableDetect := getEntity(t, store, frigateapp.PluginID, "front_door", "detect-enable")
	if enableDetect.Type != "button" {
		t.Fatalf("detect-enable type = %q, want button", enableDetect.Type)
	}
	if len(enableDetect.Commands) != 1 || enableDetect.Commands[0] != "frigate_camera_enable_detect" {
		t.Fatalf("detect-enable commands = %v, want frigate_camera_enable_detect", enableDetect.Commands)
	}

	entries, err := store.Query(storage.Query{Pattern: frigateapp.PluginID + ".front_door.>"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) < 16 {
		t.Fatalf("entity count = %d, want at least 16", len(entries))
	}
	if eventsCalls != 0 {
		t.Fatalf("/api/events calls = %d, want 0", eventsCalls)
	}
}

func TestMQTTUpdatesEventDerivedEntities(t *testing.T) {
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
						"objects": {"track": ["person"]}
					}
				}
			}`)
		case "/api/events":
			t.Fatalf("mqtt-driven runtime must not call /api/events")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)

	env := testkit.NewTestEnv(t)
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

	if err := app.HandleMQTTEvent([]byte(`{
		"type":"new",
		"after":{
			"id":"evt-1",
			"label":"person",
			"camera":"front_door",
			"start_time":1710000000,
			"has_clip":true,
			"has_snapshot":true,
			"data":{"type":"object","score":0.98}
		}
	}`)); err != nil {
		t.Fatalf("HandleMQTTEvent(new): %v", err)
	}

	camera := getEntity(t, store, frigateapp.PluginID, "front_door", "camera-state")
	cameraState, ok := camera.State.(frigateapp.CameraState)
	if !ok {
		t.Fatalf("camera state type = %T, want CameraState", camera.State)
	}
	if cameraState.LastEvent == nil || cameraState.LastEvent.ID != "evt-1" {
		t.Fatalf("camera LastEvent after MQTT = %+v, want evt-1", cameraState.LastEvent)
	}

	personEvents := getEntity(t, store, frigateapp.PluginID, "front_door", "event-person")
	personState, ok := personEvents.State.(frigateapp.EventSensorState)
	if !ok {
		t.Fatalf("event state type = %T, want EventSensorState", personEvents.State)
	}
	if !personState.EventPresent || personState.LastEventID != "evt-1" {
		t.Fatalf("unexpected person event state after MQTT: %+v", personState)
	}

	allCount := getEntity(t, store, frigateapp.PluginID, "front_door", "status-all-count")
	countState, ok := allCount.State.(frigateapp.StatusSensorState)
	if !ok {
		t.Fatalf("count state type = %T, want StatusSensorState", allCount.State)
	}
	if countState.Count != 1 {
		t.Fatalf("count after MQTT = %d, want 1", countState.Count)
	}

	allActive := getEntity(t, store, frigateapp.PluginID, "front_door", "status-all-active-count")
	activeState, ok := allActive.State.(frigateapp.StatusSensorState)
	if !ok {
		t.Fatalf("active count state type = %T, want StatusSensorState", allActive.State)
	}
	if activeState.ActiveCount != 1 {
		t.Fatalf("active count after MQTT = %d, want 1", activeState.ActiveCount)
	}

	occupancy := getEntity(t, store, frigateapp.PluginID, "front_door", "status-all-occupancy")
	occupancyState, ok := occupancy.State.(frigateapp.StatusSensorState)
	if !ok {
		t.Fatalf("occupancy state type = %T, want StatusSensorState", occupancy.State)
	}
	if occupancyState.Occupancy != "Detected" {
		t.Fatalf("occupancy after MQTT = %q, want Detected", occupancyState.Occupancy)
	}
}

func TestDiscoveryCreatesDeviceRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(singleCameraConfigHandler("front_door")))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)

	env := testkit.NewTestEnv(t)
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
	device := getDevice(t, store, frigateapp.PluginID, "front_door")
	if device.ID != "front_door" || device.Plugin != frigateapp.PluginID || device.Name != "front_door" {
		t.Fatalf("device = %+v, want ID=front_door Plugin=%s Name=front_door", device, frigateapp.PluginID)
	}
	if len(device.Entities) != 0 {
		t.Fatalf("device.Entities = %v, want empty", device.Entities)
	}
}

func TestDiscoveryCreatesOneDevicePerCamera(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(multiCameraConfigHandler("front_door", "driveway", "garage")))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)

	env := testkit.NewTestEnv(t)
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
	for _, id := range []string{"front_door", "driveway", "garage"} {
		getDevice(t, store, frigateapp.PluginID, id)
	}

	entries, err := store.Search(frigateapp.PluginID + ".*")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	deviceCount := 0
	for _, e := range entries {
		if strings.Count(e.Key, ".") == 1 {
			deviceCount++
		}
	}
	if deviceCount != 3 {
		t.Fatalf("2-part device record count = %d, want 3", deviceCount)
	}
}

func TestReconcileRemovesStaleCamera(t *testing.T) {
	var cameras atomic.Pointer[[]string]
	initial := []string{"front_door", "driveway", "garage"}
	cameras.Store(&initial)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" {
			http.NotFound(w, r)
			return
		}
		multiCameraConfigHandler(*cameras.Load()...)(w, r)
	}))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)

	env := testkit.NewTestEnv(t)
	env.Start("messenger")
	env.Start("storage")

	app1 := frigateapp.New()
	if _, err := app1.OnStart(map[string]json.RawMessage{
		"messenger": env.MessengerPayload(),
	}); err != nil {
		t.Fatalf("first OnStart: %v", err)
	}
	app1.OnShutdown()

	store := env.Storage()
	getDevice(t, store, frigateapp.PluginID, "driveway")

	reduced := []string{"front_door", "garage"}
	cameras.Store(&reduced)

	app2 := frigateapp.New()
	if _, err := app2.OnStart(map[string]json.RawMessage{
		"messenger": env.MessengerPayload(),
	}); err != nil {
		t.Fatalf("second OnStart: %v", err)
	}
	defer app2.OnShutdown()

	if _, err := store.Get(domain.DeviceKey{Plugin: frigateapp.PluginID, ID: "driveway"}); err == nil {
		t.Fatal("stale device plugin-frigate.driveway should have been deleted")
	}
	stale, err := store.Search(frigateapp.PluginID + ".driveway.>")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale driveway entities = %d, want 0", len(stale))
	}

	getDevice(t, store, frigateapp.PluginID, "front_door")
	getDevice(t, store, frigateapp.PluginID, "garage")
}

func TestDeviceAndEntitiesCoexist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(singleCameraConfigHandler("front_door")))
	defer server.Close()

	t.Setenv("FRIGATE_URL", server.URL)

	env := testkit.NewTestEnv(t)
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
	getDevice(t, store, frigateapp.PluginID, "front_door")
	getEntity(t, store, frigateapp.PluginID, "front_door", "camera-state")
}

func TestReconcileIntervalIsTenMinutes(t *testing.T) {
	if frigateapp.ReconcileInterval != 10*time.Minute {
		t.Fatalf("reconcile interval = %v, want 10m", frigateapp.ReconcileInterval)
	}
}

func singleCameraConfigHandler(name string) http.HandlerFunc {
	return multiCameraConfigHandler(name)
}

func multiCameraConfigHandler(names ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" {
			http.NotFound(w, r)
			return
		}
		cams := make(map[string]any, len(names))
		for _, n := range names {
			cams[n] = map[string]any{
				"name":      n,
				"enabled":   true,
				"detect":    map[string]any{"enabled": true},
				"motion":    map[string]any{"enabled": true},
				"record":    map[string]any{"enabled": false},
				"snapshots": map[string]any{"enabled": true},
				"objects":   map[string]any{"track": []string{"person"}},
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"cameras": cams})
	}
}

func getDevice(t *testing.T, store storage.Storage, plugin, id string) domain.Device {
	t.Helper()
	raw, err := store.Get(domain.DeviceKey{Plugin: plugin, ID: id})
	if err != nil {
		t.Fatalf("Get device %s.%s: %v", plugin, id, err)
	}
	var device domain.Device
	if err := json.Unmarshal(raw, &device); err != nil {
		t.Fatalf("Unmarshal device %s.%s: %v", plugin, id, err)
	}
	return device
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
