package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestPluginDiscovery(t *testing.T) {
	// 1. Mock Frigate API
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"front":{"enabled":true,"name":"Front Camera","detect":{"enabled":true},"record":{"enabled":false}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{"cameras":{"front":{"camera_fps":15.0,"process_fps":14.5}}}`)
		case "/api/streams":
			fmt.Fprintln(w, `{"front":{"producers":[{"url":"rtsp://frigate/front","remote_addr":"10.0.0.1"}]}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()

	// 2. Setup Plugin
	p := NewPlugin()
	sink := &mockEventSink{}
	
	// Mock environment
	_ = os.Setenv("FRIGATE_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")

	_, _ = p.OnInitialize(runner.Config{EventSink: sink}, types.Storage{})

	// 3. Trigger Discovery
	p.discover()

	// 4. Verify Discovered Map
	p.mu.RLock()
	cam, ok := p.discovered["front"]
	p.mu.RUnlock()

	if !ok {
		t.Fatal("expected 'front' camera to be discovered")
	}
	if cam.Stats.CameraFPS != 15.0 {
		t.Errorf("expected 15.0 FPS, got %f", cam.Stats.CameraFPS)
	}

	// 5. Verify Event Emission
	if len(sink.events) == 0 {
		t.Fatal("expected at least one event to be emitted")
	}

	var found bool
	for _, evt := range sink.events {
		if evt.DeviceID == "frigate-device-front" {
			found = true
			var state CameraState
			json.Unmarshal(evt.Payload, &state)
			if state.StreamURL != "rtsp://frigate/front" {
				t.Errorf("unexpected stream_url: %v", state.StreamURL)
			}
		}
	}
	if !found {
		t.Error("event for 'front' camera not found")
	}

	// 6. Test Devices List
	devices, _ := p.OnDevicesList(nil)
	if len(devices) != 1 || devices[0].ID != "frigate-device-front" {
		t.Errorf("unexpected device list: %v", devices)
	}

	// 7. Test Entities List
	entities, _ := p.OnEntitiesList("frigate-device-front", nil)
	if len(entities) != 1 || entities[0].ID != "frigate-entity-front" {
		t.Errorf("unexpected entity list: %v", entities)
	}
}
