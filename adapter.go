package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/slidebolt/plugin-frigate/pkg/frigate"
	runner "github.com/slidebolt/sdk-runner"
	"github.com/slidebolt/sdk-types"
)

const (
	domainStream       = "stream"
	domainImage        = "image"
	domainFrigateEvent = "sensor.frigate_event"
	domainFrigateHA    = "sensor.frigate_ha"
)

var haLabels = []string{"bird", "car", "cat", "dog", "package", "person"}

type discoveredCamera struct {
	Name       string                          `json:"name"`
	Config     frigate.CameraConfig            `json:"config"`
	Stats      frigate.CameraStats             `json:"stats"`
	RTCStream  frigate.RTCStreamInfo           `json:"rtc_stream"`
	LastEvents map[string]frigate.FrigateEvent `json:"last_events"`
	AllEvents  []frigate.FrigateEvent          `json:"all_events"`
	Online     bool                            `json:"online"`
	LastSeen   time.Time                       `json:"last_seen"`
}

type PluginConfig struct {
	FrigateURL string `json:"frigate_url"`
	Go2RTCURL  string `json:"go2rtc_url"`
	// Optional externally reachable base URLs used for emitted stream/image URLs.
	FrigatePublicURL string `json:"frigate_public_url"`
	Go2RTCPublicURL  string `json:"go2rtc_public_url"`

	MQTTHost        string `json:"mqtt_host"`
	MQTTPort        int    `json:"mqtt_port"`
	MQTTUser        string `json:"mqtt_user"`
	MQTTPassword    string `json:"mqtt_password"`
	MQTTTopicPrefix string `json:"mqtt_topic_prefix"`
}

type PluginFrigatePlugin struct {
	mu        sync.RWMutex
	client    frigate.Client
	pluginCtx runner.PluginContext
	pConfig   PluginConfig

	ctx    context.Context
	cancel context.CancelFunc

	mqttMu    sync.RWMutex
	mqttState map[string]map[string]string
	mqtt      *frigate.MQTTRuntime
}

type streamState struct {
	Type       string           `json:"type"`
	URL        string           `json:"url"`
	Format     string           `json:"format,omitempty"`
	Kind       string           `json:"kind,omitempty"`
	Online     bool             `json:"online"`
	SyncStatus types.SyncStatus `json:"sync_status,omitempty"`
}

type imageState struct {
	Type       string           `json:"type"`
	URL        string           `json:"url"`
	Format     string           `json:"format,omitempty"`
	Online     bool             `json:"online"`
	SyncStatus types.SyncStatus `json:"sync_status,omitempty"`
}

type frigateEventState struct {
	Type         string           `json:"type"`
	Camera       string           `json:"camera"`
	Label        string           `json:"label"`
	LastEventID  string           `json:"last_event_id"`
	LastEventAt  string           `json:"last_event_time"`
	HasSnapshot  bool             `json:"has_snapshot"`
	HasClip      bool             `json:"has_clip"`
	EventPresent bool             `json:"event_present"`
	SyncStatus   types.SyncStatus `json:"sync_status,omitempty"`
}

type frigateHASensorState struct {
	Type        string           `json:"type"`
	Value       string           `json:"value,omitempty"`
	Count       int              `json:"count,omitempty"`
	ActiveCount int              `json:"active_count,omitempty"`
	Occupancy   string           `json:"occupancy,omitempty"`
	Available   bool             `json:"available"`
	SyncStatus  types.SyncStatus `json:"sync_status,omitempty"`
}

// availabilityState represents the state of a binary_sensor.availability entity
type availabilityState struct {
	Type       string           `json:"type"`
	Available  bool             `json:"available"`
	SyncStatus types.SyncStatus `json:"sync_status,omitempty"`
}

type streamSpec struct {
	Suffix   string
	Local    string
	Path     string
	Format   string
	Kind     string
	IsOnline bool
}

type labelAgg struct {
	Count       int
	ActiveCount int
	LastEvent   *frigate.FrigateEvent
}

func NewPlugin() *PluginFrigatePlugin {
	return &PluginFrigatePlugin{
		mqttState: make(map[string]map[string]string),
	}
}

func pluginDomains() []types.DomainDescriptor {
	return []types.DomainDescriptor{
		{
			Domain:   domainFrigateEvent,
			Commands: []types.ActionDescriptor{},
			Events: []types.ActionDescriptor{
				{
					Action: "observed",
					Fields: []types.FieldDescriptor{
						{Name: "camera", Type: "string", Required: true},
						{Name: "label", Type: "string", Required: true},
						{Name: "last_event_id", Type: "string", Required: false},
						{Name: "last_event_time", Type: "string", Required: false},
						{Name: "has_snapshot", Type: "bool", Required: true},
						{Name: "has_clip", Type: "bool", Required: true},
						{Name: "event_present", Type: "bool", Required: true},
					},
				},
			},
		},
		{
			Domain:   domainFrigateHA,
			Commands: []types.ActionDescriptor{},
			Events: []types.ActionDescriptor{
				{
					Action: "updated",
					Fields: []types.FieldDescriptor{
						{Name: "value", Type: "string", Required: false},
						{Name: "count", Type: "int", Required: false},
						{Name: "active_count", Type: "int", Required: false},
						{Name: "occupancy", Type: "string", Required: false},
						{Name: "available", Type: "bool", Required: true},
					},
				},
			},
		},
	}
}

func (p *PluginFrigatePlugin) Initialize(ctx runner.PluginContext) (types.Manifest, error) {
	p.pluginCtx = ctx
	if ctx.Registry != nil {
		if stored, ok := ctx.Registry.LoadState(); ok && len(stored.Data) > 0 {
			var s struct {
				Config PluginConfig `json:"config"`
			}
			if err := json.Unmarshal(stored.Data, &s); err == nil {
				p.pConfig = s.Config
			}
		}
	}

	// ENV overrides persisted state.
	if fURL := firstEnv("FRIGATE_URL", "PLUGIN_FRIGATE_URL", "PLUGIN_FRIGATE_FRIGATE_URL"); fURL != "" {
		p.pConfig.FrigateURL = fURL
	}
	if rtcURL := firstEnv("FRIGATE_GO2RTC_URL", "FRIGATE_RTC_URL", "GO2RTC_URL", "PLUGIN_FRIGATE_GO2RTC_URL"); rtcURL != "" {
		p.pConfig.Go2RTCURL = rtcURL
	}
	if fPublicURL := firstEnv("FRIGATE_PUBLIC_URL", "PLUGIN_FRIGATE_PUBLIC_URL", "PLUGIN_FRIGATE_FRIGATE_PUBLIC_URL"); fPublicURL != "" {
		p.pConfig.FrigatePublicURL = fPublicURL
	}
	if rtcPublicURL := firstEnv("FRIGATE_GO2RTC_PUBLIC_URL", "FRIGATE_RTC_PUBLIC_URL", "GO2RTC_PUBLIC_URL", "PLUGIN_FRIGATE_GO2RTC_PUBLIC_URL"); rtcPublicURL != "" {
		p.pConfig.Go2RTCPublicURL = rtcPublicURL
	}
	if v := firstEnv("FRIGATE_MQTT_HOST", "MQTT_HOST", "PLUGIN_FRIGATE_MQTT_HOST"); v != "" {
		p.pConfig.MQTTHost = v
	}
	if v := firstEnv("FRIGATE_MQTT_PORT", "MQTT_PORT", "PLUGIN_FRIGATE_MQTT_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.pConfig.MQTTPort = n
		}
	}
	if v := firstEnv("FRIGATE_MQTT_USER", "MQTT_USER", "PLUGIN_FRIGATE_MQTT_USER"); v != "" {
		p.pConfig.MQTTUser = v
	}
	if v := firstEnv("FRIGATE_MQTT_PASSWORD", "MQTT_PASSWORD", "PLUGIN_FRIGATE_MQTT_PASSWORD"); v != "" {
		p.pConfig.MQTTPassword = v
	}
	if v := firstEnv("FRIGATE_MQTT_TOPIC_PREFIX", "MQTT_TOPIC_PREFIX", "PLUGIN_FRIGATE_MQTT_TOPIC_PREFIX"); v != "" {
		p.pConfig.MQTTTopicPrefix = v
	}
	if p.pConfig.MQTTTopicPrefix == "" {
		p.pConfig.MQTTTopicPrefix = "frigate"
	}
	if p.pConfig.MQTTPort == 0 {
		p.pConfig.MQTTPort = 1883
	}

	if p.pConfig.FrigateURL != "" {
		p.client = frigate.NewClient(p.pConfig.FrigateURL, p.pConfig.Go2RTCURL)
	}

	schemas := append([]types.DomainDescriptor{}, types.CoreDomains()...)
	schemas = append(schemas, pluginDomains()...)

	return types.Manifest{
		ID:      "plugin-frigate",
		Name:    "Frigate Video",
		Version: "1.1.0",
		Schemas: schemas,
	}, nil
}

func (p *PluginFrigatePlugin) Start(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(context.Background())

	// Register the core management device and entities.
	if p.pluginCtx.Registry != nil {
		coreID := types.CoreDeviceID("plugin-frigate")
		_ = p.pluginCtx.Registry.SaveDevice(types.Device{
			ID:         coreID,
			SourceID:   coreID,
			SourceName: "Frigate Plugin",
		})
		for _, need := range types.CoreEntities("plugin-frigate") {
			_ = p.pluginCtx.Registry.SaveEntity(need)
		}

		// System device that holds availability and config entities.
		existing, _ := p.pluginCtx.Registry.LoadDevice("frigate-system")
		_ = p.pluginCtx.Registry.SaveDevice(reconcileDevice(existing, types.Device{
			ID:         "frigate-system",
			SourceID:   "system",
			SourceName: "Frigate System",
		}))
		_ = p.pluginCtx.Registry.SaveEntity(types.Entity{
			ID:        "frigate-config",
			DeviceID:  "frigate-system",
			Domain:    "config",
			LocalName: "Frigate Config",
		})
		// Availability reflects whether Frigate is reachable.
		avail := p.client != nil
		systemEnt := types.Entity{
			ID:        "availability",
			DeviceID:  "frigate-system",
			Domain:    "binary_sensor",
			LocalName: "Frigate Availability",
		}
		setReported(&systemEnt, availabilityState{
			Type:       "state_changed",
			Available:  avail,
			SyncStatus: types.SyncStatusSynced,
		})
		_ = p.pluginCtx.Registry.SaveEntity(systemEnt)
	}

	if strings.TrimSpace(p.pConfig.MQTTHost) != "" {
		rt, err := frigate.StartMQTT(frigate.MQTTConfig{
			Host:        p.pConfig.MQTTHost,
			Port:        p.pConfig.MQTTPort,
			User:        p.pConfig.MQTTUser,
			Password:    p.pConfig.MQTTPassword,
			TopicPrefix: p.pConfig.MQTTTopicPrefix,
		}, func(camera, key, payload string) {
			p.updateMQTTState(camera, key, payload)
		})
		if err != nil {
			log.Printf("Frigate Plugin: MQTT disabled (connect failed): %v", err)
		} else {
			p.mqttMu.Lock()
			p.mqtt = rt
			p.mqttMu.Unlock()
		}
	}

	if p.client == nil {
		log.Println("Frigate Plugin: FRIGATE_URL not set, discovery disabled")
		return nil
	}

	// Initial discovery in background so Start returns quickly.
	go p.syncCameras(p.ctx)

	// Periodic refresh loop.
	go p.refreshLoop(p.ctx)

	return nil
}

func (p *PluginFrigatePlugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.mqttMu.Lock()
	rt := p.mqtt
	p.mqtt = nil
	p.mqttMu.Unlock()
	if rt != nil {
		rt.Stop()
	}
	return nil
}

func (p *PluginFrigatePlugin) OnReset() error {
	if p.pluginCtx.Registry == nil {
		return nil
	}
	for _, dev := range p.pluginCtx.Registry.LoadDevices() {
		_ = p.pluginCtx.Registry.DeleteDevice(dev.ID)
	}
	return p.pluginCtx.Registry.DeleteState()
}

// syncCameras queries Frigate once and saves all cameras/entities to the registry.
func (p *PluginFrigatePlugin) syncCameras(ctx context.Context) {
	cameras, err := p.discover()
	if err != nil {
		log.Printf("Frigate Plugin: discovery failed: %v", err)
		return
	}
	p.saveCamerasToRegistry(cameras)
}

// refreshLoop periodically re-discovers cameras and updates the registry.
func (p *PluginFrigatePlugin) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cameras, err := p.discover()
			if err != nil {
				log.Printf("Frigate Plugin: refresh failed: %v", err)
				continue
			}
			p.saveCamerasToRegistry(cameras)
		}
	}
}

// saveCamerasToRegistry writes all discovered cameras and their entities to the registry.
func (p *PluginFrigatePlugin) saveCamerasToRegistry(cameras []*discoveredCamera) {
	if p.pluginCtx.Registry == nil {
		return
	}

	// Update system availability to reflect Frigate is reachable.
	systemEnt := types.Entity{
		ID:        "availability",
		DeviceID:  "frigate-system",
		Domain:    "binary_sensor",
		LocalName: "Frigate Availability",
	}
	setReported(&systemEnt, availabilityState{
		Type:       "state_changed",
		Available:  true,
		SyncStatus: types.SyncStatusSynced,
	})
	_ = p.pluginCtx.Registry.SaveEntity(systemEnt)

	for _, cam := range cameras {
		deviceID := p.deviceID(cam.Name)

		existing, _ := p.pluginCtx.Registry.LoadDevice(deviceID)
		_ = p.pluginCtx.Registry.SaveDevice(reconcileDevice(existing, types.Device{
			ID:         deviceID,
			SourceID:   cam.Name,
			SourceName: cam.Name,
		}))

		// Availability entity for this camera.
		avEnt := types.Entity{
			ID:        "availability",
			DeviceID:  deviceID,
			Domain:    "binary_sensor",
			LocalName: cam.Name + " Availability",
		}
		setReported(&avEnt, availabilityState{
			Type:       "state_changed",
			Available:  cam.Online,
			SyncStatus: types.SyncStatusSynced,
		})
		_ = p.pluginCtx.Registry.SaveEntity(avEnt)

		// Stream entities.
		for _, spec := range p.streamSpecs(cam.Name, cam.Online) {
			ent := types.Entity{
				ID:        p.streamEntityID(cam.Name, spec.Suffix),
				DeviceID:  deviceID,
				Domain:    domainStream,
				LocalName: spec.Local,
			}
			setReported(&ent, streamState{
				Type:       "state_changed",
				URL:        p.go2rtcURLf(spec.Path, cam.Name),
				Format:     spec.Format,
				Kind:       spec.Kind,
				Online:     spec.IsOnline,
				SyncStatus: types.SyncStatusSynced,
			})
			_ = p.pluginCtx.Registry.SaveEntity(ent)
		}

		// Frame image entity.
		img := types.Entity{
			ID:        p.imageEntityID(cam.Name, "frame-jpeg"),
			DeviceID:  deviceID,
			Domain:    domainImage,
			LocalName: "Frame JPEG",
		}
		setReported(&img, imageState{
			Type:       "state_changed",
			URL:        p.go2rtcURLf("/api/frame.jpeg?src=%s", cam.Name),
			Format:     "jpeg",
			Online:     cam.Online,
			SyncStatus: types.SyncStatusSynced,
		})
		_ = p.pluginCtx.Registry.SaveEntity(img)

		// Event sensor entities.
		for _, label := range []string{"person", "car"} {
			localLabel := strings.ToUpper(label[:1]) + label[1:]
			ent := types.Entity{
				ID:        p.sensorEntityID(cam.Name, label),
				DeviceID:  deviceID,
				Domain:    domainFrigateEvent,
				LocalName: localLabel + " Events",
			}
			setReported(&ent, p.frigateEventState(cam, label))
			_ = p.pluginCtx.Registry.SaveEntity(ent)
		}

		// HA sensor entities.
		haEntities := make(map[string]types.Entity)
		p.addHAEntitiesForCamera(haEntities, cam, deviceID)
		for _, ent := range haEntities {
			_ = p.pluginCtx.Registry.SaveEntity(ent)
		}

		// Emit stream event for the main stream.
		p.emitMainStreamEvent(cam)
	}
}

// discoverDevices returns the current list of devices: the system device, the
// core plugin device, and one device per enabled Frigate camera. If the Frigate
// API is unreachable the base devices are still returned.
func (p *PluginFrigatePlugin) discoverDevices() ([]types.Device, error) {
	coreID := types.CoreDeviceID("plugin-frigate")
	byID := map[string]types.Device{
		coreID: {ID: coreID, SourceID: coreID, SourceName: "Frigate Plugin"},
		"frigate-system": {ID: "frigate-system", SourceID: "system", SourceName: "Frigate System"},
	}

	cameras, err := p.discover()
	if err == nil {
		for _, cam := range cameras {
			id := p.deviceID(cam.Name)
			byID[id] = types.Device{ID: id, SourceID: cam.Name, SourceName: cam.Name}
		}
	}

	out := make([]types.Device, 0, len(byID))
	for _, dev := range byID {
		out = append(out, dev)
	}
	return out, nil
}

// entitiesForDevice returns all entities for the given device ID by running a
// live Frigate discovery. It mirrors what saveCamerasToRegistry writes to the
// registry but returns the slice directly — useful for unit tests.
func (p *PluginFrigatePlugin) entitiesForDevice(deviceID string) ([]types.Entity, error) {
	byID := make(map[string]types.Entity)

	if deviceID == "frigate-system" {
		byID["frigate-config"] = types.Entity{
			ID: "frigate-config", DeviceID: deviceID,
			Domain: "config", LocalName: "Frigate Config",
		}
	}

	cameras, err := p.discover()
	if err != nil {
		if deviceID == "frigate-system" {
			avEnt := types.Entity{ID: "availability", DeviceID: deviceID, Domain: "binary_sensor", LocalName: "Frigate Availability"}
			setReported(&avEnt, availabilityState{Type: "state_changed", Available: false, SyncStatus: types.SyncStatusSynced})
			byID["availability"] = avEnt
		}
	} else {
		if deviceID == "frigate-system" {
			avEnt := types.Entity{ID: "availability", DeviceID: deviceID, Domain: "binary_sensor", LocalName: "Frigate Availability"}
			setReported(&avEnt, availabilityState{Type: "state_changed", Available: true, SyncStatus: types.SyncStatusSynced})
			byID["availability"] = avEnt
		}
		for _, cam := range cameras {
			if p.deviceID(cam.Name) != deviceID {
				continue
			}
			avEnt := types.Entity{ID: "availability", DeviceID: deviceID, Domain: "binary_sensor", LocalName: cam.Name + " Availability"}
			setReported(&avEnt, availabilityState{Type: "state_changed", Available: cam.Online, SyncStatus: types.SyncStatusSynced})
			byID["availability"] = avEnt

			for _, spec := range p.streamSpecs(cam.Name, cam.Online) {
				ent := types.Entity{ID: p.streamEntityID(cam.Name, spec.Suffix), DeviceID: deviceID, Domain: domainStream, LocalName: spec.Local}
				setReported(&ent, streamState{Type: "state_changed", URL: p.go2rtcURLf(spec.Path, cam.Name), Format: spec.Format, Kind: spec.Kind, Online: spec.IsOnline, SyncStatus: types.SyncStatusSynced})
				byID[ent.ID] = ent
			}

			img := types.Entity{ID: p.imageEntityID(cam.Name, "frame-jpeg"), DeviceID: deviceID, Domain: domainImage, LocalName: "Frame JPEG"}
			setReported(&img, imageState{Type: "state_changed", URL: p.go2rtcURLf("/api/frame.jpeg?src=%s", cam.Name), Format: "jpeg", Online: cam.Online, SyncStatus: types.SyncStatusSynced})
			byID[img.ID] = img

			for _, label := range []string{"person", "car"} {
				localLabel := strings.ToUpper(label[:1]) + label[1:]
				ent := types.Entity{ID: p.sensorEntityID(cam.Name, label), DeviceID: deviceID, Domain: domainFrigateEvent, LocalName: localLabel + " Events"}
				setReported(&ent, p.frigateEventState(cam, label))
				byID[ent.ID] = ent
			}

			p.addHAEntitiesForCamera(byID, cam, deviceID)
		}
	}

	for _, need := range types.CoreEntities("plugin-frigate") {
		if need.DeviceID == deviceID {
			if _, exists := byID[need.ID]; !exists {
				byID[need.ID] = need
			}
		}
	}

	out := make([]types.Entity, 0, len(byID))
	for _, ent := range byID {
		out = append(out, ent)
	}
	return out, nil
}

// reconcileDevice merges incoming discovery data into the existing device,
// preserving user-set fields (LocalName) per the sdk-types contract.
func reconcileDevice(existing, incoming types.Device) types.Device {
	result := incoming
	if existing.LocalName != "" {
		result.LocalName = existing.LocalName
	}
	return result
}

func newestEventsByCameraLabel(events []frigate.FrigateEvent) map[string]map[string]frigate.FrigateEvent {
	byCamera := make(map[string]map[string]frigate.FrigateEvent)
	for _, evt := range events {
		if strings.TrimSpace(evt.Camera) == "" || strings.TrimSpace(evt.Label) == "" {
			continue
		}
		camEvents, ok := byCamera[evt.Camera]
		if !ok {
			camEvents = make(map[string]frigate.FrigateEvent)
			byCamera[evt.Camera] = camEvents
		}
		current, exists := camEvents[evt.Label]
		if !exists || evt.StartTime > current.StartTime {
			camEvents[evt.Label] = evt
		}
	}
	return byCamera
}

// discover queries Frigate and returns camera data — stateless, no storage.
func (p *PluginFrigatePlugin) discover() ([]*discoveredCamera, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil {
		return nil, frigate.ErrOffline
	}

	cfg, err := client.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", frigate.ErrAPIFailure, err)
	}

	stats, _ := client.GetStats()
	streams, _ := client.GetRTCStreams()
	events, _ := client.GetEvents(1000)
	eventIndex := newestEventsByCameraLabel(events)

	var cameras []*discoveredCamera

	for name, camCfg := range cfg.Cameras {
		if !camCfg.Enabled {
			continue
		}

		cam := &discoveredCamera{
			Name:       name,
			Config:     camCfg,
			Online:     true,
			LastSeen:   time.Now(),
			LastEvents: make(map[string]frigate.FrigateEvent),
		}

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
		if byLabel, ok := eventIndex[name]; ok {
			cam.LastEvents = byLabel
		}
		for _, evt := range events {
			if evt.Camera == name {
				cam.AllEvents = append(cam.AllEvents, evt)
			}
		}

		cameras = append(cameras, cam)
	}

	return cameras, nil
}

func (p *PluginFrigatePlugin) updateMQTTState(camera, key, payload string) {
	camera = strings.TrimSpace(camera)
	key = strings.Trim(strings.TrimSpace(key), "/")
	if camera == "" || key == "" {
		return
	}
	p.mqttMu.Lock()
	if p.mqttState[camera] == nil {
		p.mqttState[camera] = make(map[string]string)
	}
	p.mqttState[camera][key] = strings.TrimSpace(payload)
	p.mqttMu.Unlock()
}

func (p *PluginFrigatePlugin) mqttValue(camera, key string) (string, bool) {
	p.mqttMu.RLock()
	defer p.mqttMu.RUnlock()
	cam := p.mqttState[camera]
	if cam == nil {
		return "", false
	}
	v, ok := cam[key]
	return v, ok
}

func (p *PluginFrigatePlugin) emitMainStreamEvent(cam *discoveredCamera) {
	if p.pluginCtx.Events == nil {
		return
	}
	state := streamState{
		Type:   "state_changed",
		URL:    p.go2rtcURLf("/stream.html?src=%s", cam.Name),
		Format: "html",
		Kind:   "main",
		Online: cam.Online,
	}
	payload, _ := json.Marshal(state)
	_ = p.pluginCtx.Events.PublishEvent(types.InboundEvent{
		DeviceID: p.deviceID(cam.Name),
		EntityID: p.streamEntityID(cam.Name, "main"),
		Payload:  payload,
	})
}

func (p *PluginFrigatePlugin) emitFrigateEventSensor(cam *discoveredCamera, label string) {
	if p.pluginCtx.Events == nil {
		return
	}
	state := p.frigateEventState(cam, label)
	payload, _ := json.Marshal(state)
	_ = p.pluginCtx.Events.PublishEvent(types.InboundEvent{
		DeviceID: p.deviceID(cam.Name),
		EntityID: p.sensorEntityID(cam.Name, label),
		Payload:  payload,
	})
}

func (p *PluginFrigatePlugin) streamSpecs(_ string, online bool) []streamSpec {
	return []streamSpec{
		{Suffix: "main", Local: "Main Stream", Path: "/stream.html?src=%s", Format: "html", Kind: "main", IsOnline: online},
		{Suffix: "ui", Local: "Stream UI", Path: "/stream.html?src=%s", Format: "html", Kind: "ui", IsOnline: online},
		{Suffix: "webrtc-ui", Local: "WebRTC UI", Path: "/webrtc.html?src=%s", Format: "html", Kind: "webrtc-ui", IsOnline: online},
		{Suffix: "mp4", Local: "MP4 Stream", Path: "/api/stream.mp4?src=%s", Format: "mp4", Kind: "video", IsOnline: online},
		{Suffix: "hls", Local: "HLS Stream", Path: "/api/stream.m3u8?src=%s", Format: "hls", Kind: "video", IsOnline: online},
		{Suffix: "ts", Local: "TS Stream", Path: "/api/stream.ts?src=%s", Format: "mpegts", Kind: "video", IsOnline: online},
		{Suffix: "mjpeg", Local: "MJPEG Stream", Path: "/api/stream.mjpeg?src=%s", Format: "mjpeg", Kind: "video", IsOnline: online},
		{Suffix: "frame-mp4", Local: "Frame MP4", Path: "/api/frame.mp4?src=%s", Format: "mp4", Kind: "frame", IsOnline: online},
		{Suffix: "info", Local: "Stream Info", Path: "/api/streams?src=%s", Format: "json", Kind: "info", IsOnline: online},
	}
}

func (p *PluginFrigatePlugin) frigateEventState(cam *discoveredCamera, label string) frigateEventState {
	state := frigateEventState{
		Type:         "state_changed",
		Camera:       cam.Name,
		Label:        label,
		HasSnapshot:  false,
		HasClip:      false,
		EventPresent: false,
		SyncStatus:   types.SyncStatusSynced,
	}
	if evt, ok := cam.LastEvents[label]; ok {
		state.LastEventID = evt.ID
		state.HasSnapshot = evt.HasSnapshot
		state.HasClip = evt.HasClip
		state.EventPresent = true
		if evt.StartTime > 0 {
			state.LastEventAt = time.Unix(int64(evt.StartTime), 0).UTC().Format(time.RFC3339)
		}
	}
	return state
}

func (p *PluginFrigatePlugin) trackedLabelSet(cam *discoveredCamera) map[string]bool {
	out := make(map[string]bool)
	for _, l := range cam.Config.Objects.Track {
		out[strings.ToLower(strings.TrimSpace(l))] = true
	}
	for l := range cam.LastEvents {
		out[strings.ToLower(strings.TrimSpace(l))] = true
	}
	return out
}

func (p *PluginFrigatePlugin) eventAggByLabel(cam *discoveredCamera) map[string]labelAgg {
	out := make(map[string]labelAgg)
	for _, evt := range cam.AllEvents {
		l := strings.ToLower(strings.TrimSpace(evt.Label))
		if l == "" {
			continue
		}
		agg := out[l]
		agg.Count++
		if evt.EndTime == 0 {
			agg.ActiveCount++
		}
		if agg.LastEvent == nil || evt.StartTime > agg.LastEvent.StartTime {
			e := evt
			agg.LastEvent = &e
		}
		out[l] = agg
	}
	return out
}

func boolOn(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "on" || v == "true" || v == "1"
}

func (p *PluginFrigatePlugin) labelOccupancy(cam *discoveredCamera, label string, agg labelAgg) string {
	if v, ok := p.mqttValue(cam.Name, label); ok {
		if boolOn(v) {
			return "Detected"
		}
		return "Clear"
	}
	if agg.ActiveCount > 0 {
		return "Detected"
	}
	return "Clear"
}

func (p *PluginFrigatePlugin) allOccupancy(cam *discoveredCamera, aggByLabel map[string]labelAgg) string {
	for _, l := range haLabels {
		if p.labelOccupancy(cam, l, aggByLabel[l]) == "Detected" {
			return "Detected"
		}
	}
	return "Clear"
}

func setReported(entity *types.Entity, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	entity.Data.Reported = b
	entity.Data.Effective = b
	entity.Data.UpdatedAt = time.Now()
}

func (p *PluginFrigatePlugin) deviceID(name string) string {
	return "frigate-device-" + p.sanitize(name)
}

func (p *PluginFrigatePlugin) streamEntityID(name, suffix string) string {
	return "frigate-stream-" + p.sanitize(name) + "-" + suffix
}

func (p *PluginFrigatePlugin) imageEntityID(name, suffix string) string {
	return "frigate-image-" + p.sanitize(name) + "-" + suffix
}

func (p *PluginFrigatePlugin) sensorEntityID(name, label string) string {
	return "frigate-sensor-" + p.sanitize(name) + "-" + p.sanitize(label)
}

func (p *PluginFrigatePlugin) haEntityID(name, key string) string {
	return "frigate-ha-" + p.sanitize(name) + "-" + p.sanitize(key)
}

func (p *PluginFrigatePlugin) sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('-')
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	return out
}

func (p *PluginFrigatePlugin) go2rtcBaseURL() string {
	base := strings.TrimSpace(p.pConfig.Go2RTCPublicURL)
	if base == "" {
		base = strings.TrimSpace(p.pConfig.FrigatePublicURL)
	}
	if base == "" {
		base = strings.TrimSpace(p.pConfig.Go2RTCURL)
	}
	if base == "" {
		base = strings.TrimSpace(p.pConfig.FrigateURL)
	}
	return strings.TrimSuffix(base, "/")
}

func (p *PluginFrigatePlugin) go2rtcURLf(pathFmt, camera string) string {
	base := p.go2rtcBaseURL()
	src := url.QueryEscape(camera)
	return base + fmt.Sprintf(pathFmt, src)
}

func (p *PluginFrigatePlugin) addHASensor(byID map[string]types.Entity, deviceID, entID, name string, payload frigateHASensorState) {
	ent := types.Entity{
		ID:        entID,
		DeviceID:  deviceID,
		Domain:    domainFrigateHA,
		LocalName: name,
	}
	payload.SyncStatus = types.SyncStatusSynced
	payload.Type = "state_changed"
	setReported(&ent, payload)
	byID[ent.ID] = ent
}

func (p *PluginFrigatePlugin) addHAEntitiesForCamera(byID map[string]types.Entity, cam *discoveredCamera, deviceID string) {
	tracked := p.trackedLabelSet(cam)
	aggByLabel := p.eventAggByLabel(cam)

	allCount := 0
	allActive := 0
	for _, l := range haLabels {
		agg := aggByLabel[l]
		allCount += agg.Count
		allActive += agg.ActiveCount
	}
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "all-active-count"), "All Active Count", frigateHASensorState{
		Value:       fmt.Sprintf("%d objects", allActive),
		ActiveCount: allActive,
		Available:   true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "all-count"), "All count", frigateHASensorState{
		Value:     fmt.Sprintf("%d objects", allCount),
		Count:     allCount,
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "all-occupancy"), "All occupancy", frigateHASensorState{
		Value:     p.allOccupancy(cam, aggByLabel),
		Occupancy: p.allOccupancy(cam, aggByLabel),
		Available: true,
	})

	for _, label := range haLabels {
		agg := aggByLabel[label]
		available := tracked[label] || agg.Count > 0
		value := "Unavailable"
		if available {
			value = tsFor(agg.LastEvent)
			if agg.LastEvent == nil {
				value = "Unknown"
			}
		}

		name := strings.ToUpper(label[:1]) + label[1:]
		p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, label), name, frigateHASensorState{
			Value:     value,
			Available: available,
		})
		p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, label+"-active-count"), name+" Active Count", frigateHASensorState{
			Value:       fmt.Sprintf("%d objects", agg.ActiveCount),
			ActiveCount: agg.ActiveCount,
			Available:   available,
		})
		p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, label+"-count"), name+" count", frigateHASensorState{
			Value:     fmt.Sprintf("%d objects", agg.Count),
			Count:     agg.Count,
			Available: available,
		})
		p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, label+"-occupancy"), name+" occupancy", frigateHASensorState{
			Value:     p.labelOccupancy(cam, label, agg),
			Occupancy: p.labelOccupancy(cam, label, agg),
			Available: available,
		})
	}

	recording := "Clear"
	if boolOn(getMQTTOrDefault(p, cam.Name, "recordings", "")) || cam.Config.Record.Enabled {
		recording = "Recording"
	}
	motion := "Clear"
	if boolOn(getMQTTOrDefault(p, cam.Name, "motion", "")) {
		motion = "Motion"
	}
	reviewStatus := getMQTTOrDefault(p, cam.Name, "review_status", "NONE")
	if reviewStatus == "" {
		reviewStatus = "NONE"
	}

	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "recording"), cam.Name+" Recording", frigateHASensorState{
		Value:     recording,
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "motion"), "Motion", frigateHASensorState{
		Value:     motion,
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "review-status"), "Review Status", frigateHASensorState{
		Value:     reviewStatus,
		Available: true,
	})

	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "config-detect"), "Detect", frigateHASensorState{
		Value:     boolString(cam.Config.Detect.Enabled),
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "config-motion"), "Motion (Config)", frigateHASensorState{
		Value:     boolString(cam.Config.Motion.Enabled),
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "config-recordings"), "Recordings", frigateHASensorState{
		Value:     boolString(cam.Config.Record.Enabled),
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "config-review-alerts"), "Review Alerts", frigateHASensorState{
		Value:     boolString(cam.Config.Review.Alerts.Enabled),
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "config-review-detections"), "Review Detections", frigateHASensorState{
		Value:     boolString(cam.Config.Review.Detections.Enabled),
		Available: true,
	})
	p.addHASensor(byID, deviceID, p.haEntityID(cam.Name, "config-snapshots"), "Snapshots", frigateHASensorState{
		Value:     boolString(cam.Config.Snapshots.Enabled),
		Available: true,
	})
}

func boolString(v bool) string {
	if v {
		return "On"
	}
	return "Off"
}

func getMQTTOrDefault(p *PluginFrigatePlugin, camera, key, fallback string) string {
	if v, ok := p.mqttValue(camera, key); ok {
		return v
	}
	return fallback
}

func tsFor(evt *frigate.FrigateEvent) string {
	if evt == nil || evt.StartTime <= 0 {
		return "Unavailable"
	}
	return time.Unix(int64(evt.StartTime), 0).UTC().Format(time.RFC3339)
}

func (p *PluginFrigatePlugin) runCommand(req types.Command, entity types.Entity) error {
	if entity.ID != "frigate-config" {
		return nil
	}
	var params struct {
		FrigateURL       string `json:"frigate_url"`
		Go2RTCURL        string `json:"go2rtc_url"`
		FrigatePublicURL string `json:"frigate_public_url"`
		Go2RTCPublicURL  string `json:"go2rtc_public_url"`
	}
	raw, err := json.Marshal(req.Payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return err
	}
	p.mu.Lock()
	if params.FrigateURL != "" {
		p.pConfig.FrigateURL = params.FrigateURL
	}
	if params.Go2RTCURL != "" {
		p.pConfig.Go2RTCURL = params.Go2RTCURL
	}
	if params.FrigatePublicURL != "" {
		p.pConfig.FrigatePublicURL = params.FrigatePublicURL
	}
	if params.Go2RTCPublicURL != "" {
		p.pConfig.Go2RTCPublicURL = params.Go2RTCPublicURL
	}
	if p.pConfig.FrigateURL != "" {
		p.client = frigate.NewClient(p.pConfig.FrigateURL, p.pConfig.Go2RTCURL)
	}
	if p.pluginCtx.Registry != nil {
		data := struct {
			Config PluginConfig `json:"config"`
		}{Config: p.pConfig}
		if encoded, mErr := json.Marshal(data); mErr == nil {
			_ = p.pluginCtx.Registry.SaveState(types.Storage{Data: encoded})
		}
	}
	p.mu.Unlock()

	// Kick off a fresh discovery now that the URL has changed.
	if p.ctx != nil {
		go p.syncCameras(p.ctx)
	}
	return nil
}

func (p *PluginFrigatePlugin) OnCommand(req types.Command, entity types.Entity) error {
	return p.runCommand(req, entity)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
