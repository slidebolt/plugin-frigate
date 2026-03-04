package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	Name       string                  `json:"name"`
	Config     CameraConfig            `json:"config"`
	Stats      CameraStats             `json:"stats"`
	RTCStream  RTCStreamInfo           `json:"rtc_stream"`
	LastEvents map[string]FrigateEvent `json:"last_events"`
	AllEvents  []FrigateEvent          `json:"all_events"`
	Online     bool                    `json:"online"`
	LastSeen   time.Time               `json:"last_seen"`
}

type PluginConfig struct {
	FrigateURL string `json:"frigate_url"`
	Go2RTCURL  string `json:"go2rtc_url"`

	MQTTHost        string `json:"mqtt_host"`
	MQTTPort        int    `json:"mqtt_port"`
	MQTTUser        string `json:"mqtt_user"`
	MQTTPassword    string `json:"mqtt_password"`
	MQTTTopicPrefix string `json:"mqtt_topic_prefix"`
}

type PluginFrigatePlugin struct {
	mu         sync.RWMutex
	discovered map[string]*discoveredCamera
	client     FrigateClient
	config     runner.Config
	pConfig    PluginConfig

	mqttMu    sync.RWMutex
	mqttState map[string]map[string]string
	mqtt      *mqttRuntime
}

type streamState struct {
	URL    string `json:"url"`
	Format string `json:"format,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Online bool   `json:"online"`
}

type imageState struct {
	URL    string `json:"url"`
	Format string `json:"format,omitempty"`
	Online bool   `json:"online"`
}

type frigateEventState struct {
	Camera       string `json:"camera"`
	Label        string `json:"label"`
	LastEventID  string `json:"last_event_id"`
	LastEventAt  string `json:"last_event_time"`
	HasSnapshot  bool   `json:"has_snapshot"`
	HasClip      bool   `json:"has_clip"`
	EventPresent bool   `json:"event_present"`
}

type frigateHASensorState struct {
	Value       string `json:"value,omitempty"`
	Count       int    `json:"count,omitempty"`
	ActiveCount int    `json:"active_count,omitempty"`
	Occupancy   string `json:"occupancy,omitempty"`
	Available   bool   `json:"available"`
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
	LastEvent   *FrigateEvent
}

func NewPlugin() *PluginFrigatePlugin {
	return &PluginFrigatePlugin{
		discovered: make(map[string]*discoveredCamera),
		mqttState:  make(map[string]map[string]string),
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
	for _, cam := range p.discovered {
		if cam.LastEvents == nil {
			cam.LastEvents = make(map[string]FrigateEvent)
		}
	}

	// ENV overrides persisted state.
	if fURL := firstEnv("FRIGATE_URL", "PLUGIN_FRIGATE_URL", "PLUGIN_FRIGATE_FRIGATE_URL"); fURL != "" {
		p.pConfig.FrigateURL = fURL
	}
	if rtcURL := firstEnv("FRIGATE_GO2RTC_URL", "FRIGATE_RTC_URL", "GO2RTC_URL", "PLUGIN_FRIGATE_GO2RTC_URL"); rtcURL != "" {
		p.pConfig.Go2RTCURL = rtcURL
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
		p.client = NewFrigateClient(p.pConfig.FrigateURL, p.pConfig.Go2RTCURL)
	}

	schemas := append([]types.DomainDescriptor{}, types.CoreDomains()...)
	schemas = append(schemas, pluginDomains()...)

	return types.Manifest{
		ID:      "plugin-frigate",
		Name:    "Frigate Video",
		Version: "1.1.0",
		Schemas: schemas,
	}, state
}

func (p *PluginFrigatePlugin) OnReady() {
	if p.client == nil {
		log.Println("Frigate Plugin: FRIGATE_URL not set, discovery disabled")
		return
	}
	if strings.TrimSpace(p.pConfig.MQTTHost) != "" {
		rt, err := startMQTT(p.pConfig, func(camera, key, payload string) {
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
	go p.runDiscovery()
}

func (p *PluginFrigatePlugin) WaitReady(ctx context.Context) error {
	return nil
}

func (p *PluginFrigatePlugin) OnShutdown() {
	p.mqttMu.Lock()
	rt := p.mqtt
	p.mqtt = nil
	p.mqttMu.Unlock()
	if rt != nil {
		rt.stop()
	}
}

func (p *PluginFrigatePlugin) runDiscovery() {
	for {
		p.discover()
		time.Sleep(1 * time.Minute)
	}
}

func newestEventsByCameraLabel(events []FrigateEvent) map[string]map[string]FrigateEvent {
	byCamera := make(map[string]map[string]FrigateEvent)
	for _, evt := range events {
		if strings.TrimSpace(evt.Camera) == "" || strings.TrimSpace(evt.Label) == "" {
			continue
		}
		camEvents, ok := byCamera[evt.Camera]
		if !ok {
			camEvents = make(map[string]FrigateEvent)
			byCamera[evt.Camera] = camEvents
		}
		current, exists := camEvents[evt.Label]
		if !exists || evt.StartTime > current.StartTime {
			camEvents[evt.Label] = evt
		}
	}
	return byCamera
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
	events, _ := client.GetEvents(1000)
	eventIndex := newestEventsByCameraLabel(events)

	var updated []*discoveredCamera

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
		if cam.LastEvents == nil {
			cam.LastEvents = make(map[string]FrigateEvent)
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
		} else {
			cam.LastEvents = map[string]FrigateEvent{}
		}
		cam.AllEvents = cam.AllEvents[:0]
		for _, evt := range events {
			if evt.Camera == name {
				cam.AllEvents = append(cam.AllEvents, evt)
			}
		}

		updated = append(updated, cam)
	}

	// Mark stale.
	for name, cam := range p.discovered {
		if _, ok := active[name]; !ok {
			cam.Online = false
			updated = append(updated, cam)
		}
	}
	p.mu.Unlock()

	for _, cam := range updated {
		p.emitMainStreamEvent(cam)
		p.emitFrigateEventSensor(cam, "person")
		p.emitFrigateEventSensor(cam, "car")
	}
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
	if p.config.EventSink == nil {
		return
	}
	state := streamState{
		URL:    p.go2rtcURLf("/stream.html?src=%s", cam.Name),
		Format: "html",
		Kind:   "main",
		Online: cam.Online,
	}
	payload, _ := json.Marshal(state)
	_ = p.config.EventSink.EmitEvent(types.InboundEvent{
		DeviceID: p.deviceID(cam.Name),
		EntityID: p.streamEntityID(cam.Name, "main"),
		Payload:  payload,
	})
}

func (p *PluginFrigatePlugin) emitFrigateEventSensor(cam *discoveredCamera, label string) {
	if p.config.EventSink == nil {
		return
	}
	state := p.frigateEventState(cam, label)
	payload, _ := json.Marshal(state)
	_ = p.config.EventSink.EmitEvent(types.InboundEvent{
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
		Camera:       cam.Name,
		Label:        label,
		HasSnapshot:  false,
		HasClip:      false,
		EventPresent: false,
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
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "-")
}

func (p *PluginFrigatePlugin) go2rtcBaseURL() string {
	base := strings.TrimSpace(p.pConfig.Go2RTCURL)
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

	byID["frigate-system"] = types.Device{
		ID:         "frigate-system",
		SourceID:   "system",
		SourceName: "Frigate System",
	}

	coreID := types.CoreDeviceID("plugin-frigate")
	byID[coreID] = runner.ReconcileDevice(byID[coreID], types.Device{
		ID:         coreID,
		SourceID:   coreID,
		SourceName: "Frigate Plugin",
	})

	for _, cam := range p.discovered {
		id := p.deviceID(cam.Name)
		discoveredDev := types.Device{
			ID:         id,
			SourceID:   cam.Name,
			SourceName: cam.Name,
		}
		if existing, ok := byID[id]; ok {
			byID[id] = runner.ReconcileDevice(existing, discoveredDev)
		} else {
			byID[id] = runner.ReconcileDevice(types.Device{}, discoveredDev)
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

func (p *PluginFrigatePlugin) addHASensor(byID map[string]types.Entity, deviceID, entID, name string, payload frigateHASensorState) {
	ent := types.Entity{
		ID:        entID,
		DeviceID:  deviceID,
		Domain:    domainFrigateHA,
		LocalName: name,
	}
	setReported(&ent, payload)
	byID[ent.ID] = ent
}

func tsFor(evt *FrigateEvent) string {
	if evt == nil || evt.StartTime <= 0 {
		return "Unavailable"
	}
	return time.Unix(int64(evt.StartTime), 0).UTC().Format(time.RFC3339)
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

func (p *PluginFrigatePlugin) OnEntitiesList(deviceID string, current []types.Entity) ([]types.Entity, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	byID := make(map[string]types.Entity)
	for _, ent := range current {
		byID[ent.ID] = ent
	}

	if deviceID == "frigate-system" {
		byID["frigate-config"] = types.Entity{
			ID:        "frigate-config",
			DeviceID:  "frigate-system",
			Domain:    "config",
			LocalName: "Frigate Config",
		}
	}

	for _, cam := range p.discovered {
		if p.deviceID(cam.Name) != deviceID {
			continue
		}

		for _, spec := range p.streamSpecs(cam.Name, cam.Online) {
			ent := types.Entity{
				ID:        p.streamEntityID(cam.Name, spec.Suffix),
				DeviceID:  deviceID,
				Domain:    domainStream,
				LocalName: spec.Local,
			}
			setReported(&ent, streamState{
				URL:    p.go2rtcURLf(spec.Path, cam.Name),
				Format: spec.Format,
				Kind:   spec.Kind,
				Online: spec.IsOnline,
			})
			byID[ent.ID] = ent
		}

		img := types.Entity{
			ID:        p.imageEntityID(cam.Name, "frame-jpeg"),
			DeviceID:  deviceID,
			Domain:    domainImage,
			LocalName: "Frame JPEG",
		}
		setReported(&img, imageState{
			URL:    p.go2rtcURLf("/api/frame.jpeg?src=%s", cam.Name),
			Format: "jpeg",
			Online: cam.Online,
		})
		byID[img.ID] = img

		for _, label := range []string{"person", "car"} {
			localLabel := strings.ToUpper(label[:1]) + label[1:]
			ent := types.Entity{
				ID:        p.sensorEntityID(cam.Name, label),
				DeviceID:  deviceID,
				Domain:    domainFrigateEvent,
				LocalName: localLabel + " Events",
			}
			setReported(&ent, p.frigateEventState(cam, label))
			byID[ent.ID] = ent
		}

		p.addHAEntitiesForCamera(byID, cam, deviceID)
	}

	for _, need := range types.CoreEntities("plugin-frigate") {
		if deviceID == need.DeviceID {
			if _, exists := byID[need.ID]; !exists {
				byID[need.ID] = need
			}
		}
	}

	out := make([]types.Entity, 0, len(byID))
	for _, ent := range byID {
		out = append(out, ent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p *PluginFrigatePlugin) OnCommand(req types.Command, entity types.Entity) (types.Entity, error) {
	if entity.ID == "frigate-config" {
		var params struct {
			FrigateURL string `json:"frigate_url"`
			Go2RTCURL  string `json:"go2rtc_url"`
		}
		raw, err := json.Marshal(req.Payload)
		if err != nil {
			return entity, err
		}
		if err := json.Unmarshal(raw, &params); err == nil {
			p.mu.Lock()
			if params.FrigateURL != "" {
				p.pConfig.FrigateURL = params.FrigateURL
			}
			if params.Go2RTCURL != "" {
				p.pConfig.Go2RTCURL = params.Go2RTCURL
			}
			if p.pConfig.FrigateURL != "" {
				p.client = NewFrigateClient(p.pConfig.FrigateURL, p.pConfig.Go2RTCURL)
			}
			p.mu.Unlock()
			go p.discover()
		}
		return entity, nil
	}
	return entity, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func (p *PluginFrigatePlugin) OnEvent(evt types.Event, entity types.Entity) (types.Entity, error) {
	raw, err := json.Marshal(evt.Payload)
	if err != nil {
		return entity, err
	}
	entity.Data.Reported = raw
	entity.Data.Effective = raw
	entity.Data.UpdatedAt = time.Now()
	return entity, nil
}
