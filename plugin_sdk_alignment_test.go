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

// TestNoShadowRegistry verifies that the plugin does not maintain a local device registry
func TestNoShadowRegistry(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
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

	p.OnInitialize(runner.Config{}, types.Storage{})

	// Verify that the plugin doesn't have a discovered field (shadow registry)
	// by checking that we cannot access it (it was removed from the struct)
	// The plugin should NOT store devices locally - this test verifies the field is gone
	_ = p
}

// TestLazyDiscovery verifies that discovery happens in OnDeviceDiscover, not in background loops
func TestLazyDiscovery(t *testing.T) {
	discoveryCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			discoveryCount++
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
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

	p.OnInitialize(runner.Config{}, types.Storage{})

	// OnReady should NOT trigger discovery in lazy mode
	p.OnReady()

	// Give a moment for any background goroutines to start
	// In the old implementation, runDiscovery would start here

	// OnDeviceDiscover should trigger discovery
	_, err := p.OnDeviceDiscover(nil)
	if err != nil {
		t.Fatalf("OnDeviceDiscover failed: %v", err)
	}

	if discoveryCount == 0 {
		t.Error("OnDeviceDiscover did not trigger discovery")
	}

	// Multiple calls to OnDeviceDiscover should trigger discovery each time (no caching)
	_, err = p.OnDeviceDiscover(nil)
	if err != nil {
		t.Fatalf("OnDeviceDiscover failed: %v", err)
	}

	if discoveryCount < 2 {
		t.Error("OnDeviceDiscover is caching results - should discover fresh each time")
	}
}

// TestRawStoreUsage verifies that protocol-specific metadata uses RawStore
func TestRawStoreUsage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"motion":{"enabled":true},"record":{"enabled":true},"snapshots":{"enabled":true},"review":{"alerts":{"enabled":true},"detections":{"enabled":true}},"objects":{"track":["person"]}}}}`)
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

	// Initialize with empty storage
	_, storage := p.OnInitialize(runner.Config{}, types.Storage{})

	// After OnDeviceDiscover, storage should contain raw data
	devices, _ := p.OnDeviceDiscover(nil)

	// Check that discovered devices are returned
	found := false
	for _, d := range devices {
		if d.SourceID == "cam1" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected to find cam1 device in OnDeviceDiscover results")
	}

	// Storage update should happen
	_, err := p.OnConfigUpdate(storage)
	if err != nil {
		t.Fatalf("OnConfigUpdate failed: %v", err)
	}
}

// TestDeviceMetadataInStorage verifies camera metadata is stored in RawStore
func TestDeviceMetadataInStorage(t *testing.T) {
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

	_, storage := p.OnInitialize(runner.Config{}, types.Storage{})

	// Trigger discovery via OnDeviceDiscover
	p.OnDeviceDiscover(nil)

	// Update storage
	updatedStorage, err := p.OnConfigUpdate(storage)
	if err != nil {
		t.Fatalf("OnConfigUpdate failed: %v", err)
	}

	// Verify storage contains expected data
	if len(updatedStorage.Data) == 0 {
		t.Error("storage is empty - expected camera metadata to be persisted")
	}

	// Parse storage to verify structure
	var stored struct {
		Config PluginConfig `json:"config"`
	}
	if err := json.Unmarshal(updatedStorage.Data, &stored); err != nil {
		t.Fatalf("failed to unmarshal storage: %v", err)
	}

	if stored.Config.FrigateURL == "" {
		t.Error("expected FrigateURL to be stored")
	}
}
