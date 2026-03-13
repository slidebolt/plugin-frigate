package main

import (
	"testing"

	"github.com/cucumber/godog"
	"github.com/slidebolt/sdk-integration-testing"
)

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
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("Godog suite failed")
	}
}
