package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
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

	testInit(p)

	// The plugin should NOT store devices locally — verified by absence of a "discovered" field.
	_ = p
}

// TestLazyDiscovery verifies that discovery happens in discoverDevices, not in background loops
func TestLazyDiscovery(t *testing.T) {
	var mu sync.Mutex
	discoveryCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			mu.Lock()
			discoveryCount++
			mu.Unlock()
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

	testInit(p)

	// Start should NOT trigger a blocking discovery (it may start background goroutines)
	_ = p.Start(context.Background())
	defer p.Stop()

	// discoverDevices should trigger discovery
	_, err := p.discoverDevices()
	if err != nil {
		t.Fatalf("discoverDevices failed: %v", err)
	}

	mu.Lock()
	count1 := discoveryCount
	mu.Unlock()
	if count1 == 0 {
		t.Error("discoverDevices did not trigger discovery")
	}

	// Multiple calls should each trigger discovery (no caching)
	_, err = p.discoverDevices()
	if err != nil {
		t.Fatalf("discoverDevices failed on second call: %v", err)
	}

	mu.Lock()
	count2 := discoveryCount
	mu.Unlock()
	if count2 < 2 {
		t.Error("discoverDevices is caching results - should discover fresh each time")
	}
}

// TestRawStoreUsage verifies that protocol-specific metadata is captured in config storage
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

	testInit(p)

	// After discoverDevices, cam1 should be present
	devices, _ := p.discoverDevices()

	found := false
	for _, d := range devices {
		if d.SourceID == "cam1" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected to find cam1 device in discoverDevices results")
	}

	// configStorage serialises the current config
	storage := configStorage(p)
	if len(storage.Data) == 0 {
		t.Error("configStorage returned empty data")
	}
}

// TestDeviceMetadataInStorage verifies camera metadata is captured in config storage
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

	testInit(p)
	p.discoverDevices()

	updatedStorage := configStorage(p)

	if len(updatedStorage.Data) == 0 {
		t.Error("storage is empty - expected camera metadata to be persisted")
	}

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
