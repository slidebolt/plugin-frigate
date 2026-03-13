# plugin-frigate Migration TODO

## Status
Largest and most complex plugin (~1000 lines in `adapter.go`). HTTP + MQTT integration
with Frigate NVR. Uses `ctx.State`, `ctx.Events`, AND `ctx.Config`. Highest migration
effort — plan for careful, incremental changes with tests verifying each step.

---

## 1. Code Changes (`adapter.go`)

### 1a. `Initialize` — remove config param
```go
// BEFORE
func (p *PluginFrigatePlugin) Initialize(ctx runner.PluginContext, config types.Storage) (types.Manifest, error) {

// AFTER
func (p *PluginFrigatePlugin) Initialize(ctx runner.PluginContext) (types.Manifest, error) {
```

If `config` was used to load persisted Frigate config (camera list, MQTT settings):
```go
// In Initialize, replace config param usage with:
if ctx.Registry != nil {
    if stored, ok := ctx.Registry.LoadState(); ok && len(stored.Data) > 0 {
        if err := json.Unmarshal(stored.Data, &p.pConfig); err == nil {
            // config loaded from registry
        }
    }
}
```

### 1b. Replace `ctx.State`, `ctx.Events`, `ctx.Config`
```go
type PluginFrigatePlugin struct {
    pluginCtx runner.PluginContext
    // remove: state StateService (if stored separately)
    // remove: config ConfigService (no longer exists)
    // ... keep all existing camera/MQTT fields
}
```

**IMPORTANT**: `ctx.Events` is still in `PluginContext` — no change to event publishing.

Call site mapping — search for ALL occurrences:
| Old | New |
|-----|-----|
| `p.pluginCtx.State.UpsertDevice(dev)` | `p.pluginCtx.Registry.SaveDevice(dev)` |
| `p.pluginCtx.State.UpsertEntity(ent)` | `p.pluginCtx.Registry.SaveEntity(ent)` |
| `p.pluginCtx.Events.PublishEvent(evt)` | `p.pluginCtx.Events.PublishEvent(evt)` ✅ no change |
| `p.pluginCtx.Config.SaveConfig(storage)` | `p.pluginCtx.Registry.SaveState(storage)` |

Run `grep -n "pluginCtx\.\|p\.state\.\|p\.events\." adapter.go` to find all call sites.

### 1c. `OnReset` — wire to registry
Current `OnReset` returns `nil`. Wire it:
```go
func (p *PluginFrigatePlugin) OnReset() error {
    if p.pluginCtx.Registry == nil {
        return nil
    }
    for _, dev := range p.pluginCtx.Registry.LoadDevices() {
        _ = p.pluginCtx.Registry.DeleteDevice(dev.ID)
    }
    return p.pluginCtx.Registry.DeleteState()
}
```

### 1d. `OnConfigChange` — remove from interface compliance
The runner no longer calls this. The current `OnConfigChange` persists new config and
restarts MQTT/HTTP connections. Replace with startup-time loading from Registry:
- At `Start`: load config from `ctx.Registry.LoadState()`
- Config changes (URL updates, etc.) need another mechanism — consider a command-based
  config reload or simply require restart.

### 1e. Incremental approach (recommended for this large file)
1. First: change `Initialize` signature only → `go build ./...` should fail with type errors at all old `ctx.*` call sites → fix each one.
2. Second: remove config from Initialize, update Registry calls.
3. Third: update OnReset.
4. Run tests after each step.

---

## 2. Test Changes

### 2a. Add `integration_test.go`
```go
package main

import (
    "testing"
    "github.com/slidebolt/sdk-integration-testing"
)

const pluginID = "plugin-frigate"

func TestIntegration_PluginRegisters(t *testing.T) {
    // No FRIGATE_URL set → discovery runs but finds no cameras — that's fine.
    s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
    s.RequirePlugin(pluginID)

    plugins, err := s.Plugins()
    if err != nil {
        t.Fatalf("GET /api/plugins: %v", err)
    }
    reg, ok := plugins[pluginID]
    if !ok {
        t.Fatalf("plugin %q not in registry", pluginID)
    }
    t.Logf("registered: id=%s name=%s version=%s", pluginID, reg.Manifest.Name, reg.Manifest.Version)
}

func TestIntegration_GatewayHealthy(t *testing.T) {
    s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
    s.RequirePlugin(pluginID)

    var body map[string]any
    if err := s.GetJSON("/_internal/health", &body); err != nil {
        t.Fatalf("health check: %v", err)
    }
    if body["status"] != "ok" {
        t.Errorf("expected status=ok, got %v", body["status"])
    }
}
```

### 2b. Update `godog_test.go` — wire to sdk-integration-testing
```go
func TestFeatures(t *testing.T) {
    pluginID := "plugin-frigate"
    s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
    s.RequirePlugin(pluginID)
    baseURL := s.APIURL()

    suiteCtx := newAPIFeatureContext(t, baseURL, pluginID)
    suite := godog.TestSuite{
        Name: "plugin-frigate-features",
        ScenarioInitializer: func(ctx *godog.ScenarioContext) {
            suiteCtx.InitializeAllScenarios(ctx)
        },
        Options: &godog.Options{Format: "pretty", Paths: []string{"features"}, TestingT: t},
    }
    if suite.Run() != 0 {
        t.Fatal("Godog suite failed")
    }
}
```

### 2c. Add `sdk-integration-testing` to `go.mod`
```
require github.com/slidebolt/sdk-integration-testing v0.0.0
replace github.com/slidebolt/sdk-integration-testing => ../sdk-integration-testing
```

### 2d. Add `local_test.go` for real Frigate instance
```go
//go:build local

package main

import (
    "os"
    "testing"
    "github.com/slidebolt/sdk-integration-testing"
)

func requireFrigateLocalEnv(t *testing.T) {
    t.Helper()
    if os.Getenv("FRIGATE_URL") == "" {
        t.Skip("FRIGATE_URL not set; source plugin-frigate/.env.local before running local tests")
    }
}

func TestFrigateLocal_CameraDiscovery(t *testing.T) {
    requireFrigateLocalEnv(t)
    t.Setenv("FRIGATE_URL", os.Getenv("FRIGATE_URL"))
    s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
    s.RequirePlugin("plugin-frigate")
    // Wait for cameras to appear...
}
```

---

## 3. BDD Testing Integration

`features/` already has feature files. After updating `godog_test.go`, health-check
BDD scenarios run in CI without a Frigate instance.

Frigate with no `FRIGATE_URL` should degrade gracefully: plugin registers, no cameras
appear, health check passes. Verify this behavior doesn't panic.

For hardware-backed BDD, add scenarios using the `sdk-integration-testing` world pattern (see
`plugin-wiz/integration_test.go`) with `FRIGATE_URL` set via `t.Setenv()`.

---

## 4. Pulling `.env.local` Into the Stack

`.env.local` exists with Frigate-specific vars:
```bash
# .env.local for local testing:
FRIGATE_URL=http://192.168.1.100:5000
FRIGATE_GO2RTC_URL=http://192.168.1.100:1984
# Optional MQTT:
# FRIGATE_MQTT_HOST=192.168.1.10
# FRIGATE_MQTT_PORT=1883
```

For local Frigate tests:
```bash
source plugin-frigate/.env.local
go test -tags local -v -run TestFrigateLocal ./plugin-frigate/...
```

For CI/integration tests (no Frigate):
```bash
go test -v ./plugin-frigate/...
# No FRIGATE_URL set → plugin starts, no cameras, tests pass.
```

Env vars set via `t.Setenv()` before `integrationtesting.New()` are forwarded to the plugin:
```go
t.Setenv("FRIGATE_URL", "http://frigate.local:5000")
s := integrationtesting.New(t, "github.com/slidebolt/plugin-frigate", ".")
```

---

## 5. Expected Output

### `go test -v ./plugin-frigate/...` (no Frigate instance)
```
=== RUN   TestIntegration_PluginRegisters
    registered: id=plugin-frigate name=Frigate version=1.0.0
--- PASS: TestIntegration_PluginRegisters (8.1s)

=== RUN   TestIntegration_GatewayHealthy
--- PASS: TestIntegration_GatewayHealthy (7.9s)

=== RUN   TestFeatures
Feature: Generic HTTP Request
  Scenario: Health Check with Generic Steps
    Given I send a GET request to "/_internal/health?id=plugin-frigate"
    Then the response status code should be 200
--- PASS: TestFeatures (8.3s)

PASS
```

### `source .env.local && go test -tags local -v -run TestFrigateLocal ./plugin-frigate/...`
```
=== RUN   TestFrigateLocal_CameraDiscovery
    connecting to Frigate at http://192.168.1.100:5000...
    discovered 4 camera(s): driveway, backyard, frontdoor, garage
--- PASS: TestFrigateLocal_CameraDiscovery (15.7s)
```
