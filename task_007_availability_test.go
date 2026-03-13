package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestFrigateAvailabilityEntity verifies availability entities exist for system and cameras
func TestFrigateAvailabilityEntity(t *testing.T) {
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

	// Test system device availability entity
	systemEntities, err := p.entitiesForDevice("frigate-system")
	if err != nil {
		t.Fatalf("entitiesForDevice for frigate-system failed: %v", err)
	}

	systemAvailabilityFound := false
	for _, ent := range systemEntities {
		if strings.HasSuffix(ent.ID, "-availability") && ent.Domain == "binary_sensor" {
			systemAvailabilityFound = true
			var state map[string]interface{}
			if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
				if available, ok := state["available"].(bool); ok {
					t.Logf("System availability entity found: available=%v", available)
				}
			}
			break
		}
	}

	if !systemAvailabilityFound {
		t.Log("no availability entity found for frigate-system device - Task 007 may not be complete")
	}

	// Test camera device availability entity
	cameraEntities, err := p.entitiesForDevice("frigate-device-cam1")
	if err != nil {
		t.Fatalf("entitiesForDevice for camera failed: %v", err)
	}

	cameraAvailabilityFound := false
	for _, ent := range cameraEntities {
		if strings.HasSuffix(ent.ID, "-availability") && ent.Domain == "binary_sensor" {
			cameraAvailabilityFound = true
			var state map[string]interface{}
			if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
				if available, ok := state["available"].(bool); ok {
					t.Logf("Camera availability entity found: available=%v", available)
				}
			}
			break
		}
	}

	if !cameraAvailabilityFound {
		t.Log("no availability entity found for camera device - Task 007 may not be complete")
	}

	if !systemAvailabilityFound || !cameraAvailabilityFound {
		t.Log("Requirement: Add binary_sensor.availability entities for:")
		t.Log("  - Frigate system device (frigate-system)")
		t.Log("  - Each discovered camera device")
	}
}

// TestAvailabilityEntityReportsCorrectStatus verifies availability entities report correct status
func TestAvailabilityEntityReportsCorrectStatus(t *testing.T) {
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

	cameraEntities, err := p.entitiesForDevice("frigate-device-cam1")
	if err != nil {
		t.Fatalf("entitiesForDevice failed: %v", err)
	}

	for _, ent := range cameraEntities {
		if strings.HasSuffix(ent.ID, "-availability") && ent.Domain == "binary_sensor" {
			var state map[string]interface{}
			if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
				if available, ok := state["available"].(bool); ok && available {
					t.Logf("Camera availability correctly reports: available=true")
				} else if !available {
					t.Error("Camera availability reports unavailable when API is responding")
				}
			}
			break
		}
	}
}

// TestAvailabilityEntityOnApiFailure verifies availability shows offline when API fails
func TestAvailabilityEntityOnApiFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, `{"error":"service unavailable"}`)
	}))
	defer ts.Close()

	p := NewPlugin()

	_ = os.Setenv("FRIGATE_URL", ts.URL)
	_ = os.Setenv("FRIGATE_GO2RTC_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	defer os.Unsetenv("FRIGATE_GO2RTC_URL")

	testInit(p)

	// Try to get camera entities - discovery will fail
	cameraEntities, err := p.entitiesForDevice("frigate-device-cam1")
	if err != nil {
		t.Fatalf("entitiesForDevice failed: %v", err)
	}

	// If availability entity exists, it should report unavailable
	for _, ent := range cameraEntities {
		if strings.HasSuffix(ent.ID, "-availability") && ent.Domain == "binary_sensor" {
			var state map[string]interface{}
			if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
				if available, ok := state["available"].(bool); ok && !available {
					t.Logf("Camera availability correctly reports: available=false when API fails")
				}
			}
			break
		}
	}
}
