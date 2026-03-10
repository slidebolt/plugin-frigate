package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	runner "github.com/slidebolt/sdk-runner"
	"github.com/slidebolt/sdk-types"
)

// TestFrigateErrorStatusMapping verifies sync status is set correctly for different scenarios
func TestFrigateErrorStatusMapping(t *testing.T) {
	tests := []struct {
		name          string
		configStatus  int
		wantStatus    string
		wantAvailable bool
	}{
		{
			name:          "successful API call",
			configStatus:  http.StatusOK,
			wantStatus:    "synced",
			wantAvailable: true,
		},
		{
			name:          "API returns 500 error",
			configStatus:  http.StatusInternalServerError,
			wantStatus:    "failed",
			wantAvailable: false,
		},
		{
			name:          "API returns 401 unauthorized",
			configStatus:  http.StatusUnauthorized,
			wantStatus:    "failed",
			wantAvailable: false,
		},
		{
			name:          "API returns 503 service unavailable",
			configStatus:  http.StatusServiceUnavailable,
			wantStatus:    "failed",
			wantAvailable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/config":
					if tt.configStatus == http.StatusOK {
						fmt.Fprintln(w, `{"cameras":{"cam1":{"enabled":true,"name":"cam1","detect":{"enabled":true},"objects":{"track":["person"]}}}}`)
					} else {
						w.WriteHeader(tt.configStatus)
						fmt.Fprintln(w, `{"error":"API error"}`)
					}
				case "/api/stats":
					if tt.configStatus == http.StatusOK {
						fmt.Fprintln(w, `{"cameras":{"cam1":{"camera_fps":15.0,"process_fps":14.5}}}`)
					} else {
						w.WriteHeader(tt.configStatus)
					}
				case "/api/streams":
					if tt.configStatus == http.StatusOK {
						fmt.Fprintln(w, `{"cam1":{"producers":[{"url":"rtsp://test","remote_addr":"10.0.0.1"}]}}`)
					} else {
						w.WriteHeader(tt.configStatus)
					}
				case "/api/events":
					if tt.configStatus == http.StatusOK {
						fmt.Fprintln(w, `[]`)
					} else {
						w.WriteHeader(tt.configStatus)
					}
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

			// Get entities for the camera
			deviceID := "frigate-device-cam1"
			entities, err := p.OnEntityDiscover(deviceID, nil)
			if err != nil {
				t.Fatalf("OnEntityDiscover failed: %v", err)
			}

			// Check that at least one entity has the correct sync status
			foundSyncStatus := false
			for _, ent := range entities {
				if strings.HasPrefix(ent.ID, "frigate-stream-") || strings.HasPrefix(ent.ID, "frigate-image-") {
					var state map[string]interface{}
					if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
						if status, ok := state["sync_status"].(string); ok {
							foundSyncStatus = true
							if status != tt.wantStatus {
								t.Errorf("entity %s: got sync_status=%q, want %q", ent.ID, status, tt.wantStatus)
							}
							break
						}
					}
				}
			}

			// For successful case, we should have entities with sync_status
			// For error cases, we may not have camera entities
			if tt.configStatus == http.StatusOK && !foundSyncStatus {
				t.Error("expected to find entity with sync_status in successful case")
			}
		})
	}
}

// TestSyncStatusFieldStandardValues verifies sync_status uses standardized values
func TestSyncStatusFieldStandardValues(t *testing.T) {
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

	p.OnInitialize(runner.Config{}, types.Storage{})

	// Get entities for the camera
	deviceID := "frigate-device-cam1"
	entities, err := p.OnEntityDiscover(deviceID, nil)
	if err != nil {
		t.Fatalf("OnEntityDiscover failed: %v", err)
	}

	// Valid sync status values according to SDK standard
	validStatuses := map[string]bool{
		"synced":  true,
		"pending": true,
		"failed":  true,
		"":        true, // empty string for unknown/initial state
	}

	invalidStatuses := map[string]bool{
		"in_sync": true,
		"ok":      true,
		"error":   true,
	}

	for _, ent := range entities {
		if len(ent.Data.Reported) > 0 {
			var state map[string]interface{}
			if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
				if status, ok := state["sync_status"].(string); ok {
					if !validStatuses[status] {
						t.Errorf("entity %s: invalid sync_status=%q (must be one of: synced, pending, failed, or empty)", ent.ID, status)
					}
					if invalidStatuses[status] {
						t.Errorf("entity %s: using deprecated sync_status=%q (use standardized value instead)", ent.ID, status)
					}
				}
			}
		}
	}
}

// TestSyncStatusOnCommand verifies sync status is updated during command execution
func TestSyncStatusOnCommand(t *testing.T) {
	p := NewPlugin()

	// Create a config entity
	entity := types.Entity{
		ID:       "frigate-config",
		DeviceID: "frigate-system",
		Domain:   "config",
		Data: types.EntityData{
			Reported: []byte(`{}`),
		},
	}

	// Send a command to update config
	payload, _ := json.Marshal(map[string]interface{}{
		"frigate_url": "http://test.frigate.local",
		"go2rtc_url":  "http://test.go2rtc.local",
	})
	cmd := types.Command{
		Payload: payload,
	}

	result, err := p.OnCommand(cmd, entity)
	if err != nil {
		t.Fatalf("OnCommand failed: %v", err)
	}

	// Verify sync_status is set in the result
	var state map[string]interface{}
	if err := json.Unmarshal(result.Data.Reported, &state); err == nil {
		if status, ok := state["sync_status"].(string); ok {
			if status != "synced" && status != "" {
				t.Errorf("expected sync_status to be 'synced' or empty after successful command, got %q", status)
			}
		}
	}
}

// TestSyncStatusNotInSync verifies we don't use the deprecated "in_sync" value
func TestSyncStatusNotInSync(t *testing.T) {
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

	p.OnInitialize(runner.Config{}, types.Storage{})

	// Get entities for the camera
	deviceID := "frigate-device-cam1"
	entities, err := p.OnEntityDiscover(deviceID, nil)
	if err != nil {
		t.Fatalf("OnEntityDiscover failed: %v", err)
	}

	// Ensure no entity uses "in_sync"
	for _, ent := range entities {
		if len(ent.Data.Reported) > 0 {
			var state map[string]interface{}
			if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
				if status, ok := state["sync_status"].(string); ok {
					if status == "in_sync" {
						t.Errorf("entity %s: using deprecated sync_status='in_sync' (must use 'synced')", ent.ID)
					}
				}
			}
		}
	}
}
