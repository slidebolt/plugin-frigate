package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	runner "github.com/slidebolt/sdk-runner"
	"github.com/slidebolt/sdk-types"
)

type mockEventSink struct {
	events []types.InboundEvent
}

func (m *mockEventSink) EmitEvent(evt types.InboundEvent) error {
	m.events = append(m.events, evt)
	return nil
}

func TestCamera02EntitiesExposeFullyQualifiedManagedURLs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"camera_02":{"enabled":true,"name":"camera_02","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person","package"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{"cameras":{"camera_02":{"camera_fps":15.0,"process_fps":14.5}}}`)
		case "/api/streams":
			fmt.Fprintln(w, `{"camera_02":{"producers":[{"url":"rtsp://raw-source/should-not-be-used","remote_addr":"10.0.0.1"}]}}`)
		case "/api/events":
			fmt.Fprintln(w, `[
				{"id":"evt-person","camera":"camera_02","label":"person","start_time":1772629867.56,"end_time":1772629872.10,"has_snapshot":true,"has_clip":false},
				{"id":"evt-car","camera":"camera_02","label":"car","start_time":1772629870.10,"end_time":1772629880.00,"has_snapshot":true,"has_clip":true},
				{"id":"evt-package","camera":"camera_02","label":"package","start_time":1772629890.00,"end_time":1772629899.00,"has_snapshot":true,"has_clip":true}
			]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	p := NewPlugin()
	sink := &mockEventSink{}

	_ = os.Setenv("FRIGATE_URL", ts.URL)
	_ = os.Setenv("FRIGATE_GO2RTC_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	defer os.Unsetenv("FRIGATE_GO2RTC_URL")

	manifest, _ := p.OnInitialize(runner.Config{EventSink: sink}, types.Storage{})
	p.discover()

	devices, err := p.OnDevicesList(nil)
	if err != nil {
		t.Fatalf("OnDevicesList failed: %v", err)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	if !hasDevice(devices, "frigate-device-camera_02") {
		t.Fatalf("camera device missing: %+v", devices)
	}

	entities, err := p.OnEntitiesList("frigate-device-camera_02", nil)
	if err != nil {
		t.Fatalf("OnEntitiesList failed: %v", err)
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })

	t.Log("devices table")
	t.Log("device_id\tsource_id\tsource_name")
	for _, d := range devices {
		t.Logf("%s\t%s\t%s", d.ID, d.SourceID, d.SourceName)
	}
	t.Log("entities table")
	t.Log("entity_id\tdomain\turl")
	for _, e := range entities {
		url := ""
		var st map[string]any
		if len(e.Data.Reported) > 0 && json.Unmarshal(e.Data.Reported, &st) == nil {
			if u, _ := st["url"].(string); u != "" {
				url = u
			}
		}
		t.Logf("%s\t%s\t%s", e.ID, e.Domain, url)
	}

	expectedStreamURLs := map[string]string{
		"frigate-stream-camera_02-main":      ts.URL + "/stream.html?src=camera_02",
		"frigate-stream-camera_02-ui":        ts.URL + "/stream.html?src=camera_02",
		"frigate-stream-camera_02-webrtc-ui": ts.URL + "/webrtc.html?src=camera_02",
		"frigate-stream-camera_02-mp4":       ts.URL + "/api/stream.mp4?src=camera_02",
		"frigate-stream-camera_02-hls":       ts.URL + "/api/stream.m3u8?src=camera_02",
		"frigate-stream-camera_02-ts":        ts.URL + "/api/stream.ts?src=camera_02",
		"frigate-stream-camera_02-mjpeg":     ts.URL + "/api/stream.mjpeg?src=camera_02",
		"frigate-stream-camera_02-frame-mp4": ts.URL + "/api/frame.mp4?src=camera_02",
		"frigate-stream-camera_02-info":      ts.URL + "/api/streams?src=camera_02",
	}

	for id, wantURL := range expectedStreamURLs {
		ent := getEntity(entities, id)
		if ent == nil {
			t.Fatalf("missing stream entity: %s", id)
		}
		if ent.Domain != "stream" {
			t.Fatalf("entity %s has domain %q, want %q", id, ent.Domain, "stream")
		}
		var st map[string]any
		if err := json.Unmarshal(ent.Data.Reported, &st); err != nil {
			t.Fatalf("entity %s reported decode failed: %v", id, err)
		}
		gotURL, _ := st["url"].(string)
		if gotURL != wantURL {
			t.Fatalf("entity %s url mismatch: got=%q want=%q", id, gotURL, wantURL)
		}
	}

	image := getEntity(entities, "frigate-image-camera_02-frame-jpeg")
	if image == nil {
		t.Fatalf("missing image entity for camera_02")
	}
	if image.Domain != "image" {
		t.Fatalf("image entity domain=%q want=image", image.Domain)
	}
	var imageState map[string]any
	if err := json.Unmarshal(image.Data.Reported, &imageState); err != nil {
		t.Fatalf("image reported decode failed: %v", err)
	}
	if got := imageState["url"]; got != ts.URL+"/api/frame.jpeg?src=camera_02" {
		t.Fatalf("image url mismatch: got=%v", got)
	}

	assertFrigateEventSensor(t, entities, "frigate-sensor-camera_02-person", true, false)
	assertFrigateEventSensor(t, entities, "frigate-sensor-camera_02-car", true, true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-all-count", "3 objects", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-all-active-count", "0 objects", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-all-occupancy", "Clear", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-bird", "Unavailable", false)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-person-count", "1 objects", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-car-count", "1 objects", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-config-detect", "On", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-config-motion", "On", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-config-recordings", "On", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-config-review-alerts", "On", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-config-review-detections", "On", true)
	assertHASensorValue(t, entities, "frigate-ha-camera_02-config-snapshots", "On", true)

	if !hasSchema(manifest.Schemas, "stream") || !hasSchema(manifest.Schemas, "image") || !hasSchema(manifest.Schemas, "sensor.frigate_event") {
		t.Fatalf("manifest missing expected schemas: %#v", manifest.Schemas)
	}
	if !hasSchema(manifest.Schemas, "sensor.frigate_ha") {
		t.Fatalf("manifest missing sensor.frigate_ha schema: %#v", manifest.Schemas)
	}
}

func TestCameraURLsPreferPublicConfiguredBases(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"camera_02":{"enabled":true,"name":"camera_02","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{"cameras":{"camera_02":{"camera_fps":15.0,"process_fps":14.5}}}`)
		case "/api/streams":
			fmt.Fprintln(w, `{"camera_02":{"producers":[{"url":"rtsp://internal/stream","remote_addr":"10.0.0.1"}]}}`)
		case "/api/events":
			fmt.Fprintln(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	p := NewPlugin()

	_ = os.Setenv("FRIGATE_URL", ts.URL)
	_ = os.Setenv("FRIGATE_GO2RTC_URL", ts.URL)
	_ = os.Setenv("FRIGATE_PUBLIC_URL", "https://frigate.example")
	_ = os.Setenv("FRIGATE_GO2RTC_PUBLIC_URL", "https://rtc.example")
	defer os.Unsetenv("FRIGATE_URL")
	defer os.Unsetenv("FRIGATE_GO2RTC_URL")
	defer os.Unsetenv("FRIGATE_PUBLIC_URL")
	defer os.Unsetenv("FRIGATE_GO2RTC_PUBLIC_URL")

	p.OnInitialize(runner.Config{}, types.Storage{})
	entities, err := p.OnEntitiesList("frigate-device-camera_02", nil)
	if err != nil {
		t.Fatalf("OnEntitiesList failed: %v", err)
	}

	main := getEntity(entities, "frigate-stream-camera_02-main")
	if main == nil {
		t.Fatal("missing main stream entity")
	}
	var st map[string]any
	if err := json.Unmarshal(main.Data.Reported, &st); err != nil {
		t.Fatalf("decode main stream state failed: %v", err)
	}
	if got := st["url"]; got != "https://rtc.example/stream.html?src=camera_02" {
		t.Fatalf("main stream URL mismatch: got=%v", got)
	}

	image := getEntity(entities, "frigate-image-camera_02-frame-jpeg")
	if image == nil {
		t.Fatal("missing frame jpeg entity")
	}
	st = map[string]any{}
	if err := json.Unmarshal(image.Data.Reported, &st); err != nil {
		t.Fatalf("decode image state failed: %v", err)
	}
	if got := st["url"]; got != "https://rtc.example/api/frame.jpeg?src=camera_02" {
		t.Fatalf("image URL mismatch: got=%v", got)
	}
}

func TestFrigateConfigCommandCanUpdatePublicURLs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"camera_02":{"enabled":true,"name":"camera_02","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{"cameras":{"camera_02":{"camera_fps":15.0,"process_fps":14.5}}}`)
		case "/api/streams":
			fmt.Fprintln(w, `{"camera_02":{"producers":[{"url":"rtsp://internal/stream","remote_addr":"10.0.0.1"}]}}`)
		case "/api/events":
			fmt.Fprintln(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	p := NewPlugin()
	_ = os.Setenv("FRIGATE_URL", ts.URL)
	_ = os.Setenv("FRIGATE_GO2RTC_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	defer os.Unsetenv("FRIGATE_GO2RTC_URL")

	p.OnInitialize(runner.Config{}, types.Storage{})

	cfgEntity := types.Entity{
		ID:       "frigate-config",
		DeviceID: "frigate-system",
		Domain:   "config",
	}
	payload, _ := json.Marshal(map[string]any{
		"frigate_public_url": "https://frigate-public.example",
		"go2rtc_public_url":  "https://rtc-public.example",
	})
	_, err := p.OnCommand(types.Command{Payload: payload}, cfgEntity)
	if err != nil {
		t.Fatalf("OnCommand failed: %v", err)
	}

	entities, err := p.OnEntitiesList("frigate-device-camera_02", nil)
	if err != nil {
		t.Fatalf("OnEntitiesList failed: %v", err)
	}
	main := getEntity(entities, "frigate-stream-camera_02-main")
	if main == nil {
		t.Fatal("missing main stream entity")
	}
	var st map[string]any
	if err := json.Unmarshal(main.Data.Reported, &st); err != nil {
		t.Fatalf("decode main stream state failed: %v", err)
	}
	if got := st["url"]; got != "https://rtc-public.example/stream.html?src=camera_02" {
		t.Fatalf("main stream URL mismatch after config command: got=%v", got)
	}
}

func assertFrigateEventSensor(t *testing.T, entities []types.Entity, id string, hasSnapshot, hasClip bool) {
	t.Helper()
	ent := getEntity(entities, id)
	if ent == nil {
		t.Fatalf("missing frigate event sensor %s", id)
	}
	if ent.Domain != "sensor.frigate_event" {
		t.Fatalf("entity %s domain=%q want sensor.frigate_event", id, ent.Domain)
	}
	var st map[string]any
	if err := json.Unmarshal(ent.Data.Reported, &st); err != nil {
		t.Fatalf("decode sensor %s reported failed: %v", id, err)
	}
	if got, _ := st["has_snapshot"].(bool); got != hasSnapshot {
		t.Fatalf("sensor %s has_snapshot=%v want %v", id, got, hasSnapshot)
	}
	if got, _ := st["has_clip"].(bool); got != hasClip {
		t.Fatalf("sensor %s has_clip=%v want %v", id, got, hasClip)
	}
}

func assertHASensorValue(t *testing.T, entities []types.Entity, id, wantValue string, wantAvailable bool) {
	t.Helper()
	ent := getEntity(entities, id)
	if ent == nil {
		t.Fatalf("missing ha sensor %s", id)
	}
	if ent.Domain != "sensor.frigate_ha" {
		t.Fatalf("entity %s domain=%q want sensor.frigate_ha", id, ent.Domain)
	}
	var st map[string]any
	if err := json.Unmarshal(ent.Data.Reported, &st); err != nil {
		t.Fatalf("decode ha sensor %s failed: %v", id, err)
	}
	if got, _ := st["value"].(string); got != wantValue {
		t.Fatalf("%s value=%q want=%q", id, got, wantValue)
	}
	if got, _ := st["available"].(bool); got != wantAvailable {
		t.Fatalf("%s available=%v want=%v", id, got, wantAvailable)
	}
}

func hasDevice(devices []types.Device, id string) bool {
	for _, d := range devices {
		if d.ID == id {
			return true
		}
	}
	return false
}

func getEntity(entities []types.Entity, id string) *types.Entity {
	for i := range entities {
		if entities[i].ID == id {
			return &entities[i]
		}
	}
	return nil
}

func hasSchema(schemas []types.DomainDescriptor, domain string) bool {
	for _, s := range schemas {
		if s.Domain == domain {
			return true
		}
	}
	return false
}
