package main

import (
	"testing"

	"github.com/slidebolt/sdk-integration-testing"
)

const pluginID = "plugin-frigate"

func TestIntegration_PluginRegisters(t *testing.T) {
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
	if reg.Manifest.Name == "" {
		t.Errorf("plugin registration has empty name")
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
