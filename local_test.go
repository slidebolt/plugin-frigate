//go:build local

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/slidebolt/sdk-integration-testing"
	"github.com/slidebolt/sdk-types"
)

const frigatePluginID = "plugin-frigate"

func TestFrigateLocal_01_Discovery(t *testing.T) {
	requireFrigateLocalEnv(t)

	s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
	s.RequirePlugin(frigatePluginID)

	devices := waitForFrigateDevices(t, s, 30*time.Second)
	cameras := cameraDevices(devices)
	t.Logf("discovered %d Frigate camera(s)", len(cameras))

	ids := frigateDeviceIDs(cameras)
	for _, id := range ids {
		cam := deviceByID(cameras, id)
		t.Logf("  camera: id=%s source_id=%s source_name=%q", cam.ID, cam.SourceID, cam.SourceName)
	}

	// Also log the system device to confirm it's present.
	var sys *types.Device
	for i := range devices {
		if devices[i].ID == "frigate-system" {
			sys = &devices[i]
			break
		}
	}
	if sys == nil {
		t.Error("frigate-system device missing from registry")
	} else {
		t.Logf("  system:  id=%s source_id=%s", sys.ID, sys.SourceID)
	}
}

func TestFrigateLocal_02_CameraEntities(t *testing.T) {
	requireFrigateLocalEnv(t)

	s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
	s.RequirePlugin(frigatePluginID)

	devices := waitForFrigateDevices(t, s, 30*time.Second)
	cameras := cameraDevices(devices)
	if len(cameras) == 0 {
		t.Skip("no cameras discovered")
	}

	for _, cam := range cameras {
		var entities []types.Entity
		path := fmt.Sprintf("/api/plugins/%s/devices/%s/entities", frigatePluginID, cam.ID)
		if err := s.GetJSON(path, &entities); err != nil {
			t.Fatalf("get entities for %s: %v", cam.ID, err)
		}

		domainCounts := make(map[string]int)
		for _, ent := range entities {
			domainCounts[ent.Domain]++
		}

		t.Logf("camera %s (%s): %d entities", cam.ID, cam.SourceID, len(entities))
		domains := make([]string, 0, len(domainCounts))
		for d := range domainCounts {
			domains = append(domains, d)
		}
		sort.Strings(domains)
		for _, d := range domains {
			t.Logf("  domain=%-30s count=%d", d, domainCounts[d])
		}

		if domainCounts["stream"] == 0 {
			t.Errorf("camera %s missing stream entities", cam.ID)
		}
		if domainCounts["image"] == 0 {
			t.Errorf("camera %s missing image entities", cam.ID)
		}
		if domainCounts["binary_sensor"] == 0 {
			t.Errorf("camera %s missing binary_sensor (availability) entity", cam.ID)
		}

		// Verify all stream entities have a non-empty URL in their reported state.
		for _, ent := range entities {
			if ent.Domain != "stream" && ent.Domain != "image" {
				continue
			}
			if len(ent.Data.Reported) == 0 {
				t.Errorf("entity %s has no reported state", ent.ID)
				continue
			}
			var state map[string]any
			if err := json.Unmarshal(ent.Data.Reported, &state); err != nil {
				t.Errorf("entity %s reported is not valid JSON: %v", ent.ID, err)
				continue
			}
			url, _ := state["url"].(string)
			if url == "" {
				t.Errorf("entity %s has empty url in reported state", ent.ID)
			}
		}
	}
}

func TestFrigateLocal_03_SystemEntities(t *testing.T) {
	requireFrigateLocalEnv(t)

	s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
	s.RequirePlugin(frigatePluginID)

	// Wait for the system device to be present (it always exists).
	var systemEntities []types.Entity
	ok := s.WaitFor(15*time.Second, func() bool {
		return s.GetJSON(fmt.Sprintf("/api/plugins/%s/devices/frigate-system/entities", frigatePluginID), &systemEntities) == nil &&
			len(systemEntities) > 0
	})
	if !ok {
		t.Fatalf("frigate-system entities did not appear within 15s")
	}

	hasAvailability := false
	hasConfig := false
	for _, ent := range systemEntities {
		if ent.Domain == "binary_sensor" && ent.ID == "availability" {
			hasAvailability = true
			var state map[string]any
			if err := json.Unmarshal(ent.Data.Reported, &state); err == nil {
				t.Logf("system availability: available=%v", state["available"])
			}
		}
		if ent.Domain == "config" && ent.ID == "frigate-config" {
			hasConfig = true
		}
	}

	if !hasAvailability {
		t.Error("frigate-system missing binary_sensor/availability entity")
	}
	if !hasConfig {
		t.Error("frigate-system missing config/frigate-config entity")
	}
}

func TestFrigateLocal_04_DiscoveryAndRestart(t *testing.T) {
	requireFrigateLocalEnv(t)

	s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
	s.RequirePlugin(frigatePluginID)

	devicesBefore := waitForFrigateDevices(t, s, 30*time.Second)
	idsBefore := frigateDeviceIDs(cameraDevices(devicesBefore))
	t.Logf("before restart: %d camera(s) — %v", len(idsBefore), idsBefore)

	s.Restart()
	s.RequirePlugin(frigatePluginID)

	devicesAfter := waitForFrigateDevices(t, s, 30*time.Second)
	idsAfter := frigateDeviceIDs(cameraDevices(devicesAfter))
	t.Logf("after restart:  %d camera(s) — %v", len(idsAfter), idsAfter)

	slices.Sort(idsBefore)
	slices.Sort(idsAfter)
	if !slices.Equal(idsBefore, idsAfter) {
		t.Fatalf("camera set changed across restart: before=%v after=%v", idsBefore, idsAfter)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func requireFrigateLocalEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FRIGATE_URL"} {
		if os.Getenv(key) == "" {
			t.Skipf("%s not set; source plugin-frigate/.env.local before running local tests", key)
		}
	}
}

// waitForFrigateDevices polls until at least one camera device appears in the
// registry (distinct from the core plugin device and the system device).
func waitForFrigateDevices(t *testing.T, s *integrationtesting.Suite, timeout time.Duration) []types.Device {
	t.Helper()
	var devices []types.Device
	ok := s.WaitFor(timeout, func() bool {
		if err := s.GetJSON(fmt.Sprintf("/api/plugins/%s/devices", frigatePluginID), &devices); err != nil {
			return false
		}
		return len(cameraDevices(devices)) > 0
	})
	if !ok {
		t.Fatalf("no Frigate camera devices discovered within %s (check FRIGATE_URL)", timeout)
	}
	return devices
}

// cameraDevices filters out the core plugin device and the frigate-system device,
// returning only the per-camera devices discovered from the Frigate API.
func cameraDevices(devices []types.Device) []types.Device {
	coreID := types.CoreDeviceID(frigatePluginID)
	out := devices[:0:0]
	for _, dev := range devices {
		if dev.ID == coreID || dev.ID == "frigate-system" {
			continue
		}
		out = append(out, dev)
	}
	return out
}

func frigateDeviceIDs(devices []types.Device) []string {
	ids := make([]string, 0, len(devices))
	for _, dev := range devices {
		ids = append(ids, dev.ID)
	}
	slices.Sort(ids)
	return ids
}

func deviceByID(devices []types.Device, id string) types.Device {
	for _, dev := range devices {
		if dev.ID == id {
			return dev
		}
	}
	return types.Device{ID: id}
}

// frigateGetJSON is a thin wrapper used by tests that need plain http.Get.
func frigateGetJSON(t *testing.T, s *integrationtesting.Suite, path string, out any) {
	t.Helper()
	resp, err := http.Get(s.APIURL() + path) //nolint:noctx
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// frigateStreamSummary returns a human-readable summary of stream entities for a camera.
func frigateStreamSummary(entities []types.Entity) string {
	var parts []string
	for _, ent := range entities {
		if ent.Domain != "stream" {
			continue
		}
		var state map[string]any
		if len(ent.Data.Reported) > 0 {
			_ = json.Unmarshal(ent.Data.Reported, &state)
		}
		suffix := strings.TrimPrefix(ent.ID, "frigate-stream-")
		url, _ := state["url"].(string)
		parts = append(parts, fmt.Sprintf("%s=%s", suffix, url))
	}
	sort.Strings(parts)
	return strings.Join(parts, " | ")
}
