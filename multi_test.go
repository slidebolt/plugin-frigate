//go:build test_multi

package main

import (
	"testing"

	integrationtesting "github.com/slidebolt/sdk-integration-testing"
)

// TestMain starts one shared stack for all CI-safe multi-plugin tests.
// Existing TestIntegration_* and TestFeatures tests automatically reuse it.
func TestMain(m *testing.M) {
	integrationtesting.RunAll(m, []integrationtesting.PluginSpec{
		{Module: "github.com/slidebolt/plugin-frigate", Dir: "."},
	})
}
