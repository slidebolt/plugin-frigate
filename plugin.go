package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	runner "github.com/slidebolt/sdk-runner"
	"github.com/slidebolt/sdk-types"
)

type discoveredCamera struct {
	Name      string        `json:"name"`
	Config    CameraConfig  `json:"config"`
	Stats     CameraStats   `json:"stats"`
	RTCStream RTCStreamInfo `json:"rtc_stream"`
	Online    bool          `json:"online"`
	LastSeen  time.Time     `json:"last_seen"`
}

type PluginConfig struct {
	FrigateURL string `json:"frigate_url"`
}

type PluginFrigatePlugin struct {
	mu         sync.RWMutex
	discovered map[string]*discoveredCamera
	client     FrigateClient
	config     runner.Config
	pConfig    PluginConfig
}

func NewPlugin() *PluginFrigatePlugin {
	return &PluginFrigatePlugin{
		discovered: make(map[string]*discoveredCamera),
	}
}

func (p *PluginFrigatePlugin) OnInitialize(config runner.Config, state types.Storage) (types.Manifest, types.Storage) {
	p.config = config
	if len(state.Data) > 0 {
		var stored struct {
			Config     PluginConfig                 `json:"config"`
			Discovered map[string]*discoveredCamera `json:"discovered"`
		}
		if err := json.Unmarshal(state.Data, &stored); err == nil {
			p.pConfig = stored.Config
			p.discovered = stored.Discovered
		}
	}
	if p.discovered == nil {
		p.discovered = make(map[string]*discoveredCamera)
	}

	// ENV overrides persisted state
	if fURL := os.Getenv("FRIGATE_URL"); fURL != "" {
		p.pConfig.FrigateURL = fURL
	}

	if p.pConfig.FrigateURL != "" {
		p.client = NewFrigateClient(p.pConfig.FrigateURL, os.Getenv("FRIGATE_RTC_URL"))
	}

	return types.Manifest{
		ID:      "plugin-frigate",
		Name:    "Frigate Video",
		Version: "1.1.0",
	}, state
}

func (p *PluginFrigatePlugin) OnReady() {
	if p.client == nil {
		log.Println("Frigate Plugin: FRIGATE_URL not set, discovery disabled")
		return
	}
	go p.runDiscovery()
}

func (p *PluginFrigatePlugin) runDiscovery() {
	for {
		p.discover()
		time.Sleep(1 * time.Minute)
	}
}

func (p *PluginFrigatePlugin) discover() {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil {
		return
	}

	cfg, err := client.GetConfig()
	if err != nil {
		log.Printf("Frigate Discovery: failed to get config: %v", err)
		return
	}

	stats, _ := client.GetStats()
	streams, _ := client.GetRTCStreams()

	type update struct {
		cam    *discoveredCamera
		online bool
	}
	var updates []update

	p.mu.Lock()
	active := make(map[string]struct{})
	for name, camCfg := range cfg.Cameras {
		if !camCfg.Enabled {
			continue
		}
		active[name] = struct{}{}

		cam, ok := p.discovered[name]
		if !ok {
			cam = &discoveredCamera{Name: name}
			p.discovered[name] = cam
		}
		cam.Config = camCfg
		cam.Online = true
		cam.LastSeen = time.Now()

		if stats != nil {
			if s, ok := stats.Cameras[name]; ok {
				cam.Stats = s
			}
		}
		if streams != nil {
			if s, ok := streams[name]; ok {
				cam.RTCStream = s
			}
		}

		updates = append(updates, update{cam: cam, online: true})
	}

	// Mark stale
	for name, cam := range p.discovered {
		if _, ok := active[name]; !ok {
			if cam.Online {
				cam.Online = false
				updates = append(updates, update{cam: cam, online: false})
			}
		}
	}
	p.mu.Unlock()

	// Emit events outside of lock
	for _, u := range updates {
		p.emitStateEvent(u.cam)
	}
}

func (p *PluginFrigatePlugin) emitStateEvent(cam *discoveredCamera) {
	deviceID := p.deviceID(cam.Name)
	entityID := p.entityID(cam.Name)

	state := p.buildCameraState(cam)
	payloadBytes, _ := json.Marshal(state)

	_ = p.config.EventSink.EmitEvent(types.InboundEvent{
		DeviceID: deviceID,
		EntityID: entityID,
		Payload:  payloadBytes,
	})
}

func (p *PluginFrigatePlugin) buildCameraState(cam *discoveredCamera) CameraState {
	state := CameraState{
		Online:     cam.Online,
		Detecting:  cam.Config.Detect.Enabled,
		Recording:  cam.Config.Record.Enabled,
		FPS:        cam.Stats.CameraFPS,
		ProcessFPS: cam.Stats.ProcessFPS,
	}

	if len(cam.RTCStream.Producers) > 0 {
		state.StreamURL = cam.RTCStream.Producers[0].URL
	}

	return state
}

func (p *PluginFrigatePlugin) deviceID(name string) string {
	return "frigate-device-" + p.sanitize(name)
}

func (p *PluginFrigatePlugin) entityID(name string) string {
	return "frigate-entity-" + p.sanitize(name)
}

func (p *PluginFrigatePlugin) sanitize(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "-")
}

func (p *PluginFrigatePlugin) OnHealthCheck() (string, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil {
		return "", fmt.Errorf("FRIGATE_URL not configured")
	}
	return "perfect", nil
}

func (p *PluginFrigatePlugin) OnStorageUpdate(current types.Storage) (types.Storage, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	data := struct {
		Config     PluginConfig                 `json:"config"`
		Discovered map[string]*discoveredCamera `json:"discovered"`
	}{
		Config:     p.pConfig,
		Discovered: p.discovered,
	}

	encoded, _ := json.Marshal(data)
	current.Data = encoded
	return current, nil
}

func (p *PluginFrigatePlugin) OnDeviceCreate(dev types.Device) (types.Device, error) {
	return dev, nil
}

func (p *PluginFrigatePlugin) OnDeviceUpdate(dev types.Device) (types.Device, error) {
	return dev, nil
}

func (p *PluginFrigatePlugin) OnDeviceDelete(id string) error {
	return nil
}

func (p *PluginFrigatePlugin) OnDevicesList(current []types.Device) ([]types.Device, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	byID := make(map[string]types.Device)
	for _, dev := range current {
		byID[dev.ID] = dev
	}

	// System device for global configuration
	byID["frigate-system"] = types.Device{
		ID:         "frigate-system",
		SourceID:   "system",
		SourceName: "Frigate System",
	}

	for _, cam := range p.discovered {
		id := p.deviceID(cam.Name)
		byID[id] = types.Device{
			ID:         id,
			SourceID:   cam.Name,
			SourceName: cam.Name,
			LocalName:  cam.Name,
			Config:     types.Storage{Meta: "frigate-camera"},
		}
	}

	out := make([]types.Device, 0, len(byID))
	for _, dev := range byID {
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p *PluginFrigatePlugin) OnDeviceSearch(q types.SearchQuery, res []types.Device) ([]types.Device, error) {
	return res, nil
}

func (p *PluginFrigatePlugin) OnEntityCreate(e types.Entity) (types.Entity, error) {
	return e, nil
}

func (p *PluginFrigatePlugin) OnEntityUpdate(e types.Entity) (types.Entity, error) {
	return e, nil
}

func (p *PluginFrigatePlugin) OnEntityDelete(d, e string) error {
	return nil
}

func (p *PluginFrigatePlugin) OnEntitiesList(deviceID string, current []types.Entity) ([]types.Entity, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	byID := make(map[string]types.Entity)
	for _, ent := range current {
		byID[ent.ID] = ent
	}

	if deviceID == "frigate-system" {
		byID["frigate-config"] = types.Entity{
			ID:       "frigate-config",
			DeviceID: "frigate-system",
			Domain:   "config",
		}
	}

	for _, cam := range p.discovered {
		if p.deviceID(cam.Name) != deviceID {
			continue
		}
		id := p.entityID(cam.Name)
		
		ent := types.Entity{
			ID:        id,
			DeviceID:  deviceID,
			Domain:    CameraDomain,
			LocalName: "Camera",
			Actions:   []string{ActionStream},
		}
		
		state := p.buildCameraState(cam)
		store := BindCamera(&ent)
		_ = store.SetReported(state)

		byID[id] = ent
	}

	out := make([]types.Entity, 0, len(byID))
	for _, ent := range byID {
		out = append(out, ent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p *PluginFrigatePlugin) OnCommand(cmd types.Command, entity types.Entity) (types.Entity, error) {
	if entity.ID == "frigate-config" {
		var params struct {
			FrigateURL string `json:"frigate_url"`
		}
		if err := json.Unmarshal(cmd.Payload, &params); err == nil && params.FrigateURL != "" {
			p.mu.Lock()
			p.pConfig.FrigateURL = params.FrigateURL
			p.client = NewFrigateClient(params.FrigateURL, "")
			p.mu.Unlock()
			go p.discover()
		}
		return entity, nil
	}
	return entity, nil
}

func (p *PluginFrigatePlugin) OnEvent(evt types.Event, entity types.Entity) (types.Entity, error) {
	var state CameraState
	if err := json.Unmarshal(evt.Payload, &state); err != nil {
		return entity, err
	}
	store := BindCamera(&entity)
	_ = store.SetReported(state)
	return entity, nil
}
