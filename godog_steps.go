package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/require"
)

type apiFeatureContext struct {
	testingT  *testing.T
	baseURL   string
	client    *http.Client
	pluginID  string

	lastResponse *http.Response
	lastBody     []byte
}

func newAPIFeatureContext(t *testing.T, baseURL, pluginID string) *apiFeatureContext {
	return &apiFeatureContext{
		testingT: t,
		baseURL:  baseURL,
		pluginID: pluginID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *apiFeatureContext) resetState(sc *godog.Scenario) {
	c.lastResponse = nil
	c.lastBody = nil
}

func (c *apiFeatureContext) iMakeARequest(method, path string, body *godog.DocString) error {
	var req *http.Request
	var err error

	fullURL := c.baseURL + path
	fullURL = strings.ReplaceAll(fullURL, "{plugin_id}", c.pluginID)

	if body != nil {
		req, err = http.NewRequest(method, fullURL, bytes.NewReader([]byte(body.Content)))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, fullURL, nil)
	}
	require.NoError(c.testingT, err, "Failed to create request")

	c.lastResponse, err = c.client.Do(req)
	require.NoError(c.testingT, err, "Failed to execute request")
	
	defer c.lastResponse.Body.Close()
	c.lastBody, err = io.ReadAll(c.lastResponse.Body)
	require.NoError(c.testingT, err, "Failed to read response body")

	return nil
}

func (c *apiFeatureContext) iMakeARequestWithoutBody(method, path string) error {
	return c.iMakeARequest(method, path, nil)
}

func (c *apiFeatureContext) theResponseStatusCodeShouldBe(expected int) error {
	require.Equal(c.testingT, expected, c.lastResponse.StatusCode, "Unexpected status code")
	return nil
}

func (c *apiFeatureContext) InitializeAllScenarios(ctx *godog.ScenarioContext) {
	ctx.BeforeScenario(c.resetState)

	ctx.Step(`^I send an? (GET|POST|PUT|DELETE|PATCH) request to "([^"]*)"$`, c.iMakeARequestWithoutBody)
	ctx.Step(`^I send an? (GET|POST|PUT|DELETE|PATCH) request to "([^"]*)" with body:$`, c.iMakeARequest)
	ctx.Step(`^the response status code should be (\d+)$`, c.theResponseStatusCodeShouldBe)
}
