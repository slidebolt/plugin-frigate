//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	app "github.com/slidebolt/plugin-frigate/app"
)

// TestDiscovery_FindCameras connects to a real Frigate NVR and verifies
// at least one camera is configured. Reads FRIGATE_URL from .env.local.
//
// Run: go test -tags integration -v -run TestDiscovery_FindCameras ./cmd/plugin-frigate/
func TestDiscovery_FindCameras(t *testing.T) {
	loadEnvLocal(t)

	url := os.Getenv("FRIGATE_URL")
	if url == "" {
		t.Fatal("FRIGATE_URL not set — add it to .env.local")
	}

	t.Logf("connecting to Frigate at %s...", url)

	client := app.NewFrigateClient(url, "", "", 10*time.Second)
	cameras, err := client.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("cannot connect to Frigate at %s: %v", url, err)
	}
	if len(cameras) == 0 {
		t.Fatal("Frigate returned 0 cameras — expected at least 1")
	}

	t.Logf("found %d camera(s):", len(cameras))
	for name := range cameras {
		t.Logf("  %s", name)
	}
}
