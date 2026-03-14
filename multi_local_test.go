//go:build test_multi_local

package main

import (
	"testing"

	integrationtesting "github.com/slidebolt/sdk-integration-testing"
)

// TestMain starts one shared stack for all local-network multi-plugin tests.
func TestMain(m *testing.M) {
	integrationtesting.RunAll(m, []integrationtesting.PluginSpec{
		{Module: "github.com/slidebolt/plugin-frigate", Dir: "."},
	})
}

func TestMultiLocal_BaselineScaffold(t *testing.T) {
	t.Skip("baseline scaffold only; add real local-network scenarios as hardware workflows are defined")
}
