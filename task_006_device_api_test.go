package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestFrigateNoShadowRegistries verifies devices are properly registered and queryable
func TestFrigateNoShadowRegistries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"camera_fps":15.0,"process_fps":14.5}}}`)
		case "/api/streams":
			fmt.Fprintln(w, `{"cam1":{"producers":[{"url":"rtsp://test","remote_addr":"10.0.0.1"}]}}`)
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

	testInit(p)

	// Trigger discovery
	devices, err := p.discoverDevices()
	if err != nil {
		t.Fatalf("discoverDevices failed: %v", err)
	}

	foundCam := false
	for _, dev := range devices {
		if dev.SourceID == "cam1" {
			foundCam = true
			break
		}
	}

	if !foundCam {
		t.Error("device not found via API - may be stored in shadow registry instead of RawStore")
	}

	// Query devices again - should still find camera (no caching, fresh query each time)
	devices, err = p.discoverDevices()
	if err != nil {
		t.Fatalf("discoverDevices failed on second call: %v", err)
	}

	foundCam = false
	for _, dev := range devices {
		if dev.SourceID == "cam1" {
			foundCam = true
			break
		}
	}

	if !foundCam {
		t.Error("camera device not found on second query")
	}
}

// TestFrigateRawStorePersistence verifies devices are reflected in config storage
func TestFrigateRawStorePersistence(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{}`)
		case "/api/streams":
			fmt.Fprintln(w, `{}`)
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

	testInit(p)

	// Trigger discovery
	_, _ = p.discoverDevices()

	// Verify config storage contains data
	updatedStorage := configStorage(p)
	if len(updatedStorage.Data) == 0 {
		t.Error("storage is empty - expected camera metadata to be persisted to RawStore")
	}

	var stored struct {
		Config PluginConfig `json:"config"`
	}
	if err := json.Unmarshal(updatedStorage.Data, &stored); err != nil {
		t.Fatalf("failed to unmarshal storage data: %v", err)
	}

	if stored.Config.FrigateURL == "" {
		t.Error("expected FrigateURL to be stored in RawStore")
	}
}

// TestJSONResponseFormat verifies entity responses have valid JSON format
func TestJSONResponseFormat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"objects":{"track":["person"]}}}}`)
		case "/api/stats":
			fmt.Fprintln(w, `{}`)
		case "/api/streams":
			fmt.Fprintln(w, `{}`)
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

	testInit(p)

	deviceID := "frigate-device-cam1"
	entities, err := p.entitiesForDevice(deviceID)
	if err != nil {
		t.Fatalf("entitiesForDevice failed: %v", err)
	}

	for _, ent := range entities {
		if len(ent.Data.Reported) > 0 {
			var state map[string]interface{}
			if err := json.Unmarshal(ent.Data.Reported, &state); err != nil {
				t.Errorf("failed to decode entity %s: %v", ent.ID, err)
				continue
			}

			for key, val := range state {
				if key == "id" || key == "camera" || key == "label" || key == "value" {
					if _, ok := val.(float64); ok {
						t.Errorf("entity %s: field %s is number, should be string", ent.ID, key)
					}
				}
			}
		}
	}
}
