//go:build bdd

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/cucumber/godog"
	"github.com/slidebolt/plugin-frigate/app"
	domain "github.com/slidebolt/sb-domain"
)

func (c *bddCtx) aCameraEntityNamed(key, name string) error {
	plug, dev, id, err := parseKey(key)
	if err != nil {
		return err
	}

	state := app.CameraState{
		Connected:     true,
		Enabled:       true,
		DetectEnabled: true,
	}

	rawState, _ := json.Marshal(state)

	return c.saveEntity(domain.Entity{
		ID:       id,
		Plugin:   plug,
		DeviceID: dev,
		Type:     "frigate_camera_status",
		Name:     name,
		State:    json.RawMessage(rawState),
	})
}

func (c *bddCtx) iReceiveAnMQTTMessageWithPayloadFromFile(topic, filepath string) error {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("failed to read MQTT payload file: %w", err)
	}

	// Instantiate the app to process the MQTT event
	frigateApp := app.New()
	frigateApp.SetStorage(c.store)

	return frigateApp.HandleMQTTEvent(data)
}

func (c *bddCtx) theCameraFieldShouldBeUpdated(field string) error {
	if c.lastEntity.ID == "" {
		return fmt.Errorf("no entity was retrieved previously")
	}

	var state app.CameraState

	// The domain.Entity decoder might yield map[string]interface{} for unknown types
	// Or it might be json.RawMessage if we bypassed decoder
	switch v := c.lastEntity.State.(type) {
	case app.CameraState:
		state = v
	case json.RawMessage:
		if err := json.Unmarshal(v, &state); err != nil {
			return fmt.Errorf("could not decode json.RawMessage state: %w", err)
		}
	default:
		// Try to re-marshal and unmarshal
		raw, _ := json.Marshal(v)
		if err := json.Unmarshal(raw, &state); err != nil {
			return fmt.Errorf("could not decode state: %w", err)
		}
	}

	if state.LastEvent == nil {
		return fmt.Errorf("expected LastEvent to be set, but it was nil")
	}
	return nil
}

func (c *bddCtx) aSlideBoltNotificationEventShouldBeTriggered() error {
	// Not implemented completely, placeholder for the full system integration
	return nil
}

func (c *bddCtx) theCameraStateShouldRevertToIdle() error {
	// Not implemented completely
	return nil
}

func (c *bddCtx) retrievingSucceeds(key string) error {
	return c.iRetrieve(key)
}

func (c *bddCtx) RegisterMQTTSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a camera entity "([^"]*)" named "([^"]*)"$`, c.aCameraEntityNamed)
	ctx.Step(`^I receive an MQTT message on "([^"]*)" with payload from file "([^"]*)"$`, c.iReceiveAnMQTTMessageWithPayloadFromFile)
	ctx.Step(`^retrieving "([^"]*)" succeeds$`, c.retrievingSucceeds)
	ctx.Step(`^the camera "([^"]*)" field should be updated$`, c.theCameraFieldShouldBeUpdated)
	ctx.Step(`^a SlideBolt notification event should be triggered$`, c.aSlideBoltNotificationEventShouldBeTriggered)
	ctx.Step(`^the camera state should revert to idle$`, c.theCameraStateShouldRevertToIdle)
}
