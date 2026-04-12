//go:build integration || local

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	app "github.com/slidebolt/plugin-frigate/app"
)

// TestSimpleDiscovery_Mocked demonstrates how a Frigate discovery test works
// by mocking the Frigate NVR HTTP API.
//
// This allows proving the "real-world" discovery logic (HTTP requests, parsing,
// data mapping) without requiring a physical camera during the test run.
func TestSimpleDiscovery_Mocked(t *testing.T) {
	// 1. Setup a Mock Frigate Server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the plugin is hitting the expected endpoint
		if r.URL.Path != "/api/config" {
			t.Errorf("Unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Simulate a real Frigate config response
		config := map[string]interface{}{
			"cameras": map[string]interface{}{
				"front_door": map[string]interface{}{
					"enabled": true,
					"detect":  map[string]bool{"enabled": true},
					"motion":  map[string]bool{"enabled": true},
				},
				"back_yard": map[string]interface{}{
					"enabled": true,
					"detect":  map[string]bool{"enabled": false},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config)
	}))
	defer mockServer.Close()

	t.Logf("Mock Frigate server running at %s", mockServer.URL)

	// 2. Initialize the Plugin Client
	// In a real device test, mockServer.URL would be replaced by FRIGATE_URL from .env.local
	client := app.NewFrigateClient(mockServer.URL, "", "", 5*time.Second)

	// 3. Perform Discovery
	t.Log("discovering cameras from Frigate...")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cameras, err := client.GetConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to fetch config: %v", err)
	}

	// 4. Validate Results
	if len(cameras) != 2 {
		t.Fatalf("Expected 2 cameras, found %d", len(cameras))
	}

	t.Logf("✓ Found %d cameras:", len(cameras))
	for name, cfg := range cameras {
		status := "disabled"
		if cfg.Enabled {
			status = "enabled"
		}
		t.Logf("  - %s [%s] (detect: %v)", name, status, cfg.Detect.Enabled)
	}

	// Specific Assertions
	if _, ok := cameras["front_door"]; !ok {
		t.Error("Missing 'front_door' camera")
	}
	if cameras["front_door"].Detect.Enabled != true {
		t.Error("'front_door' should have detection enabled")
	}
	if cameras["back_yard"].Detect.Enabled != false {
		t.Error("'back_yard' should have detection disabled")
	}
}
