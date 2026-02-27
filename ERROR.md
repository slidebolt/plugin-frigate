# ERROR: TestFrigateDiscovery — 404 on wrong command URL

## Failing test
`testrunner/integration/plugins/plugin-frigate/discovery_test.go`
`TestFrigateDiscovery`

## Symptom
```
discovery_test.go:50: config update command failed with status 404
FAIL  github.com/slidebolt/testrunner/integration/plugins/plugin-frigate
```

## Root cause
`discovery_test.go:44` posts to a non-existent gateway route:

```go
// WRONG — this route does not exist in the gateway
resp, err := http.Post(testutil.APIBaseURL()+"/api/commands", ...)
```

The gateway has no `/api/commands` endpoint. The standard command route is:

```
POST /api/plugins/:id/devices/:did/entities/:eid/commands
```

## What needs fixing
`discovery_test.go` must use the correct command URL and body format:

```go
// Correct URL
url := testutil.APIBaseURL() + "/api/plugins/plugin-frigate/devices/frigate-internal/entities/internal-config/commands"

// Correct body — only the payload field, no device_id/entity_id wrapping
payload := map[string]any{
    "frigate_url": ts.URL,
}
body, _ := json.Marshal(payload)
resp, err := http.Post(url, "application/json", bytes.NewReader(body))
// expect 202 Accepted, not 200
```

The plugin code itself is correct: `plugin.go:OnCommand` handles the `internal-config` entity and reads `frigate_url` from the command payload to update the client. Only the test URL is wrong.

## File to fix
`testrunner/integration/plugins/plugin-frigate/discovery_test.go`
Lines 43-50
