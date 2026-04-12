//go:build integration || local

package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/slidebolt/plugin-frigate/app"
	domain "github.com/slidebolt/sb-domain"
	testkit "github.com/slidebolt/sb-testkit"
	storage "github.com/slidebolt/sb-storage-sdk"
)

// TestSystemMethod_PhysicalDiscovery proves that the "real world" testing
// method works by spinning up a mock SlideBolt manager and running the
// actual plugin in-process against the physical Frigate NVR.
func TestSystemMethod_PhysicalDiscovery(t *testing.T) {
	// 1. Load REAL physical connection details from .env.local
	// (Uses the loadEnvLocal helper defined in test.integration.discovery_test.go)
	loadEnvLocal(t)
	url := os.Getenv("FRIGATE_URL")
	if url == "" {
		t.Skip("FRIGATE_URL not set in .env.local — skipping physical test")
	}

	t.Logf("SYSTEM METHOD: Starting mock manager and in-process plugin for %s", url)

	// 2. Setup the mock SlideBolt manager environment
	// This creates a real NATS-based messenger bus and a storage server in-process.
	env := testkit.NewTestEnv(t)
	env.Start("messenger")
	env.Start("storage")
	store := env.Storage()

	// 3. Start the ACTUAL plugin in-process
	// We pass the messenger payload so the plugin connects to our mock manager.
	p := app.New()
	deps := map[string]json.RawMessage{
		"messenger": env.MessengerPayload(),
	}
	if _, err := p.OnStart(deps); err != nil {
		t.Fatalf("plugin OnStart failed: %v", err)
	}
	t.Cleanup(func() { p.OnShutdown() })

	// 4. Verification: Poll the mock storage until the physical cameras appear.
	// This proves that the plugin:
	//   - Connected to the real Frigate NVR
	//   - Authenticated and parsed the real JSON config
	//   - Generated SlideBolt entities
	//   - Successfully saved them to the mock Storage server over the Messenger bus
	t.Log("Waiting for plugin to discover and register physical cameras...")
	
	deadline := time.After(15 * time.Second)
	var entities []storage.Entry
	for {
		// Query all entities belonging to this plugin
		entities, _ = store.Query(storage.Query{
			Where: []storage.Filter{{Field: "plugin", Op: storage.Eq, Value: "plugin-frigate"}},
		})
		
		if len(entities) > 0 {
			break
		}

		select {
		case <-deadline:
			t.Fatal("TIMEOUT: Plugin failed to discover physical cameras within 15s")
		case <-time.After(500 * time.Millisecond):
			// Keep polling
		}
	}

	// 5. Success!
	t.Logf("✓ SUCCESS: Plugin discovered and registered %d physical entities:", len(entities))
	for _, e := range entities {
		var ent domain.Entity
		json.Unmarshal(e.Data, &ent)
		t.Logf("  - %s (Type: %s)", e.Key, ent.Type)
	}
}
