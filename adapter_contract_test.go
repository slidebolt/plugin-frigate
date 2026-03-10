package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	runner "github.com/slidebolt/sdk-runner"
	"github.com/slidebolt/sdk-types"
)

func TestFrigateSystemAvailabilityEntity_UsesCanonicalID(t *testing.T) {
	p := NewPlugin()

	entities, err := p.OnEntityDiscover("frigate-system", nil)
	if err != nil {
		t.Fatalf("OnEntityDiscover failed: %v", err)
	}

	for _, e := range entities {
		if e.Domain == "binary_sensor" && e.ID == "availability" {
			return
		}
	}
	t.Fatalf("expected frigate-system to include binary_sensor entity with id=availability")
}

func TestFrigateDiscovery_ReturnsQueryableDevice(t *testing.T) {
	p := NewPlugin()
	mockCam := "unit-cam"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			_, _ = w.Write([]byte(`{"cameras":{"unit-cam":{"enabled":true,"name":"Unit Cam","detect":{"enabled":true}}}}`))
		case "/api/stats":
			_, _ = w.Write([]byte(`{"cameras":{"unit-cam":{"camera_fps":15.0}}}`))
		case "/api/streams":
			_, _ = w.Write([]byte(`{"unit-cam":{"producers":[{"url":"rtsp://mock/unit-cam"}]}}`))
		case "/api/events":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	_ = os.Setenv("FRIGATE_URL", ts.URL)
	_ = os.Setenv("FRIGATE_GO2RTC_URL", ts.URL)
	defer os.Unsetenv("FRIGATE_URL")
	defer os.Unsetenv("FRIGATE_GO2RTC_URL")

	p.OnInitialize(runner.Config{}, types.Storage{})
	devices, err := p.OnDeviceDiscover(nil)
	if err != nil {
		t.Fatalf("OnDeviceDiscover failed: %v", err)
	}

	wantID := p.deviceID(mockCam)
	for _, d := range devices {
		if d.ID == wantID {
			return
		}
	}
	t.Fatalf("expected discovered device %q to be present", wantID)
}
