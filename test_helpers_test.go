package main

import (
	"encoding/json"

	runner "github.com/slidebolt/sdk-runner"
	"github.com/slidebolt/sdk-types"
)

// testEventPublisher is a mock runner.EventService that records emitted events.
type testEventPublisher struct {
	events []types.InboundEvent
}

func (m *testEventPublisher) PublishEvent(evt types.InboundEvent) error {
	m.events = append(m.events, evt)
	return nil
}

// testInit calls Initialize with an empty PluginContext (no registry, no events).
func testInit(p *PluginFrigatePlugin) types.Manifest {
	manifest, _ := p.Initialize(runner.PluginContext{})
	return manifest
}

// testInitWithEvents calls Initialize with the given EventService.
func testInitWithEvents(p *PluginFrigatePlugin, pub runner.EventService) types.Manifest {
	manifest, _ := p.Initialize(runner.PluginContext{Events: pub})
	return manifest
}

// configStorage serialises the plugin's current pConfig as a types.Storage blob.
// It replaces the old OnConfigUpdate shim that was used to inspect persisted config.
func configStorage(p *PluginFrigatePlugin) types.Storage {
	p.mu.RLock()
	defer p.mu.RUnlock()
	data := struct {
		Config PluginConfig `json:"config"`
	}{Config: p.pConfig}
	encoded, _ := json.Marshal(data)
	return types.Storage{Data: encoded}
}
