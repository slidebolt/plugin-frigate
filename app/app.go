// plugin-frigate integrates Frigate NVR with SlideBolt.
//
// Features:
//   - Frigate HTTP API integration
//   - Camera discovery from Frigate configuration
//   - Motion detection event monitoring
//   - Snapshot and clip retrieval
//   - Review item tracking
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	contract "github.com/slidebolt/sb-contract"
	domain "github.com/slidebolt/sb-domain"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

const PluginID = "plugin-frigate"

type FrigateConfig struct {
	URL        string `json:"url"`
	Go2RTCURL  string `json:"go2rtc_url,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	Timeout    int    `json:"timeout_ms,omitempty"`
	EventLimit int    `json:"event_limit,omitempty"`
}

type FeatureToggle struct {
	Enabled bool `json:"enabled"`
}

func (f *FeatureToggle) UnmarshalJSON(data []byte) error {
	var enabled bool
	if err := json.Unmarshal(data, &enabled); err == nil {
		f.Enabled = enabled
		return nil
	}

	var aux struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	f.Enabled = aux.Enabled
	return nil
}

type CameraConfig struct {
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	Detect     FeatureToggle   `json:"detect"`
	Motion     FeatureToggle   `json:"motion"`
	Record     FeatureToggle   `json:"record"`
	Snap       FeatureToggle   `json:"snapshots"`
	Review     ReviewConfig    `json:"review,omitempty"`
	Objects    ObjectConfig    `json:"objects,omitempty"`
	MotionMask []interface{}   `json:"motion_mask,omitempty"`
	Zones      map[string]Zone `json:"zones,omitempty"`
}

type Zone struct {
	Name        string   `json:"name"`
	Coordinates any      `json:"coordinates,omitempty"`
	Objects     []string `json:"objects,omitempty"`
}

type ReviewConfig struct {
	Alerts     FeatureToggle `json:"alerts,omitempty"`
	Detections FeatureToggle `json:"detections,omitempty"`
}

type ObjectConfig struct {
	Track []string `json:"track,omitempty"`
}

type Event struct {
	ID                 string    `json:"id"`
	Label              string    `json:"label"`
	Camera             string    `json:"camera"`
	StartTime          float64   `json:"start_time"`
	EndTime            float64   `json:"end_time,omitempty"`
	FalsePositive      bool      `json:"false_positive"`
	Zones              []string  `json:"zones"`
	HasClip            bool      `json:"has_clip"`
	HasSnapshot        bool      `json:"has_snapshot"`
	RetainIndefinitely bool      `json:"retain_indefinitely"`
	Data               EventData `json:"data"`
}

type EventData struct {
	Type     string    `json:"type"`
	Score    float64   `json:"score,omitempty"`
	TopScore float64   `json:"top_score,omitempty"`
	Region   []float64 `json:"region,omitempty"`
	Box      []float64 `json:"box,omitempty"`
}

type CameraState struct {
	Connected        bool     `json:"connected"`
	Enabled          bool     `json:"enabled"`
	DetectEnabled    bool     `json:"detect_enabled"`
	RecordEnabled    bool     `json:"record_enabled"`
	SnapshotsEnabled bool     `json:"snapshots_enabled"`
	Zones            []string `json:"zones"`
	LastEvent        *Event   `json:"last_event,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
}

type AvailabilityState struct {
	Available bool `json:"available"`
}

type StreamState struct {
	URL    string `json:"url"`
	Format string `json:"format,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Online bool   `json:"online"`
}

type ImageState struct {
	URL    string `json:"url"`
	Format string `json:"format,omitempty"`
	Online bool   `json:"online"`
}

type EventSensorState struct {
	Camera       string `json:"camera"`
	Label        string `json:"label"`
	LastEventID  string `json:"last_event_id,omitempty"`
	LastEventAt  string `json:"last_event_time,omitempty"`
	HasSnapshot  bool   `json:"has_snapshot"`
	HasClip      bool   `json:"has_clip"`
	EventPresent bool   `json:"event_present"`
}

type StatusSensorState struct {
	Value       string `json:"value,omitempty"`
	Count       int    `json:"count,omitempty"`
	ActiveCount int    `json:"active_count,omitempty"`
	Occupancy   string `json:"occupancy,omitempty"`
	Available   bool   `json:"available"`
}

type CameraEnableDetect struct{}
type CameraDisableDetect struct{}
type CameraEnableRecord struct{}
type CameraDisableRecord struct{}
type CameraEnableSnapshots struct{}
type CameraDisableSnapshots struct{}

func init() {
	domain.Register("frigate_camera", CameraState{})
	domain.Register("frigate_availability", AvailabilityState{})
	domain.Register("frigate_stream", StreamState{})
	domain.Register("frigate_image", ImageState{})
	domain.Register("frigate_event_sensor", EventSensorState{})
	domain.Register("frigate_status_sensor", StatusSensorState{})
	domain.RegisterCommand("frigate_camera_enable_detect", CameraEnableDetect{})
	domain.RegisterCommand("frigate_camera_disable_detect", CameraDisableDetect{})
	domain.RegisterCommand("frigate_camera_enable_record", CameraEnableRecord{})
	domain.RegisterCommand("frigate_camera_disable_record", CameraDisableRecord{})
	domain.RegisterCommand("frigate_camera_enable_snapshots", CameraEnableSnapshots{})
	domain.RegisterCommand("frigate_camera_disable_snapshots", CameraDisableSnapshots{})
}

type FrigateClient struct {
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client
}

func NewFrigateClient(baseURL, username, password string, timeout time.Duration) *FrigateClient {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &FrigateClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Username:   username,
		Password:   password,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (c *FrigateClient) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	if c.Username != "" && c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}

	return c.HTTPClient.Do(req)
}

func (c *FrigateClient) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if c.Username != "" && c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}

	return c.HTTPClient.Do(req)
}

func (c *FrigateClient) GetConfig(ctx context.Context) (map[string]CameraConfig, error) {
	resp, err := c.get(ctx, "/api/config")
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get config: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var config struct {
		Cameras map[string]CameraConfig `json:"cameras"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	return config.Cameras, nil
}

func (c *FrigateClient) GetEvents(ctx context.Context, camera string, limit int) ([]Event, error) {
	path := fmt.Sprintf("/api/events?limit=%d", limit)
	if camera != "" {
		path += fmt.Sprintf("&cameras=%s", camera)
	}

	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get events: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}

	return events, nil
}

func (c *FrigateClient) GetSnapshot(ctx context.Context, camera string) ([]byte, error) {
	path := fmt.Sprintf("/api/%s/latest.jpg", camera)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get snapshot: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

func (c *FrigateClient) SetDetect(ctx context.Context, camera string, enabled bool) error {
	path := fmt.Sprintf("/api/%s/detect/%s", camera, strconv.FormatBool(enabled))
	resp, err := c.post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("set detect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set detect: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *FrigateClient) SetRecord(ctx context.Context, camera string, enabled bool) error {
	path := fmt.Sprintf("/api/%s/record/%s", camera, strconv.FormatBool(enabled))
	resp, err := c.post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("set record: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set record: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *FrigateClient) SetSnapshots(ctx context.Context, camera string, enabled bool) error {
	path := fmt.Sprintf("/api/%s/snapshots/%s", camera, strconv.FormatBool(enabled))
	resp, err := c.post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("set snapshots: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set snapshots: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

type App struct {
	msg    messenger.Messenger
	store  storage.Storage
	cmds   *messenger.Commands
	subs   []messenger.Subscription
	config FrigateConfig
	client *FrigateClient
	ctx    context.Context
	cancel context.CancelFunc
}

type labelAgg struct {
	Count       int
	ActiveCount int
	LastEvent   *Event
}

type streamSpec struct {
	ID     string
	Name   string
	Path   string
	Format string
	Kind   string
}

func New() *App { return &App{} }

func (a *App) Hello() contract.HelloResponse {
	return contract.HelloResponse{
		ID:              PluginID,
		Kind:            contract.KindPlugin,
		ContractVersion: contract.ContractVersion,
		DependsOn:       []string{"messenger", "storage"},
	}
}

func (a *App) OnStart(deps map[string]json.RawMessage) (json.RawMessage, error) {
	msg, err := messenger.Connect(deps)
	if err != nil {
		return nil, fmt.Errorf("connect messenger: %w", err)
	}
	a.msg = msg

	storeClient, err := storage.Connect(deps)
	if err != nil {
		return nil, fmt.Errorf("connect storage: %w", err)
	}
	a.store = storeClient

	if err := a.loadConfig(); err != nil {
		log.Printf("plugin-frigate: warning: could not load config: %v", err)
	}

	if a.config.URL != "" {
		timeout := a.config.Timeout
		if timeout == 0 {
			timeout = 30000
		}
		a.client = NewFrigateClient(
			a.config.URL,
			a.config.Username,
			a.config.Password,
			time.Duration(timeout)*time.Millisecond,
		)
	}

	a.cmds = messenger.NewCommands(msg, domain.LookupCommand)
	sub, err := a.cmds.Receive(PluginID+".>", a.handleCommand)
	if err != nil {
		return nil, fmt.Errorf("subscribe commands: %w", err)
	}
	a.subs = append(a.subs, sub)

	if a.client != nil {
		a.ctx, a.cancel = context.WithCancel(context.Background())
		if err := a.discoverCameras(); err != nil {
			log.Printf("plugin-frigate: camera discovery error: %v", err)
		}
		go a.monitorEvents(a.ctx)
		go a.reconcileCameras(a.ctx)
	}

	log.Println("plugin-frigate: started")
	return nil, nil
}

func (a *App) OnShutdown() error {
	if a.cancel != nil {
		a.cancel()
	}
	for _, sub := range a.subs {
		sub.Unsubscribe()
	}
	if a.store != nil {
		a.store.Close()
	}
	if a.msg != nil {
		a.msg.Close()
	}
	return nil
}

func (a *App) loadConfig() error {
	configJSON := os.Getenv("FRIGATE_CONFIG")
	if configJSON != "" {
		return json.Unmarshal([]byte(configJSON), &a.config)
	}

	if _, err := os.Stat("config.json"); err == nil {
		data, err := os.ReadFile("config.json")
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &a.config)
	}

	url := os.Getenv("FRIGATE_URL")
	if url != "" {
		a.config.URL = url
		a.config.Go2RTCURL = os.Getenv("FRIGATE_GO2RTC_URL")
		a.config.Username = os.Getenv("FRIGATE_USERNAME")
		a.config.Password = os.Getenv("FRIGATE_PASSWORD")
		timeout := 30000
		if t := os.Getenv("FRIGATE_TIMEOUT_MS"); t != "" {
			if ti, err := strconv.Atoi(t); err == nil {
				timeout = ti
			}
		}
		a.config.Timeout = timeout
	}
	if limit := os.Getenv("FRIGATE_EVENT_LIMIT"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 {
			a.config.EventLimit = n
		}
	}

	return nil
}

func (a *App) discoverCameras() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cameras, err := a.client.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("get config: %w", err)
	}
	limit := a.config.EventLimit
	if limit <= 0 {
		limit = 100
	}
	events, err := a.client.GetEvents(ctx, "", limit)
	if err != nil {
		log.Printf("plugin-frigate: failed to get events during discovery: %v", err)
		events = nil
	}
	aggByCamera := aggregateEventsByCamera(events)

	for name, config := range cameras {
		cameraEntity := domain.Entity{
			ID:       name,
			Plugin:   PluginID,
			DeviceID: name,
			Type:     "frigate_camera",
			Name:     fmt.Sprintf("Frigate Camera %s", name),
			Commands: []string{
				"frigate_camera_enable_detect",
				"frigate_camera_disable_detect",
				"frigate_camera_enable_record",
				"frigate_camera_disable_record",
				"frigate_camera_enable_snapshots",
				"frigate_camera_disable_snapshots",
			},
			State: a.cameraState(config, aggByCamera[name]),
		}

		if err := a.store.Save(cameraEntity); err != nil {
			log.Printf("plugin-frigate: failed to save camera entity %s: %v", name, err)
		} else {
			log.Printf("plugin-frigate: registered camera %s (enabled=%v, detect=%v)",
				name, config.Enabled, config.Detect.Enabled)
		}

		for _, entity := range a.childEntities(name, config, aggByCamera[name]) {
			if err := a.store.Save(entity); err != nil {
				log.Printf("plugin-frigate: failed to save entity %s: %v", entity.Key(), err)
			}
		}
	}

	return nil
}

func (a *App) reconcileCameras(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.discoverCameras(); err != nil {
				log.Printf("plugin-frigate: camera reconcile error: %v", err)
			}
		}
	}
}

func (a *App) monitorEvents(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkEvents()
		}
	}
}

func (a *App) checkEvents() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events, err := a.client.GetEvents(ctx, "", 10)
	if err != nil {
		log.Printf("plugin-frigate: failed to get events: %v", err)
		return
	}

	for _, event := range events {
		a.updateCameraState(event.Camera, func(s *CameraState) {
			s.LastEvent = &event
		})
	}

	aggByCamera := aggregateEventsByCamera(events)
	for camera, agg := range aggByCamera {
		for _, entity := range a.eventEntities(camera, agg) {
			if err := a.store.Save(entity); err != nil {
				log.Printf("plugin-frigate: failed to update event entity %s: %v", entity.Key(), err)
			}
		}
		for _, entity := range a.summaryEntities(camera, agg) {
			if err := a.store.Save(entity); err != nil {
				log.Printf("plugin-frigate: failed to update summary entity %s: %v", entity.Key(), err)
			}
		}
	}
}

func (a *App) handleCommand(addr messenger.Address, cmd any) {
	cameraID := addr.EntityID

	switch cmd.(type) {
	case CameraEnableDetect:
		a.handleEnableDetect(cameraID, true)
	case CameraDisableDetect:
		a.handleEnableDetect(cameraID, false)
	case CameraEnableRecord:
		a.handleEnableRecord(cameraID, true)
	case CameraDisableRecord:
		a.handleEnableRecord(cameraID, false)
	case CameraEnableSnapshots:
		a.handleEnableSnapshots(cameraID, true)
	case CameraDisableSnapshots:
		a.handleEnableSnapshots(cameraID, false)
	default:
		log.Printf("plugin-frigate: unknown command %T for %s", cmd, addr.Key())
	}
}

func (a *App) handleEnableDetect(cameraID string, enabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.client.SetDetect(ctx, cameraID, enabled); err != nil {
		log.Printf("plugin-frigate: failed to set detect for %s: %v", cameraID, err)
		a.updateCameraState(cameraID, func(s *CameraState) {
			s.LastError = err.Error()
		})
		return
	}

	log.Printf("plugin-frigate: detection %s for camera %s",
		map[bool]string{true: "enabled", false: "disabled"}[enabled], cameraID)

	a.updateCameraState(cameraID, func(s *CameraState) {
		s.DetectEnabled = enabled
		s.LastError = ""
	})
}

func (a *App) handleEnableRecord(cameraID string, enabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.client.SetRecord(ctx, cameraID, enabled); err != nil {
		log.Printf("plugin-frigate: failed to set record for %s: %v", cameraID, err)
		a.updateCameraState(cameraID, func(s *CameraState) {
			s.LastError = err.Error()
		})
		return
	}

	log.Printf("plugin-frigate: recording %s for camera %s",
		map[bool]string{true: "enabled", false: "disabled"}[enabled], cameraID)

	a.updateCameraState(cameraID, func(s *CameraState) {
		s.RecordEnabled = enabled
		s.LastError = ""
	})
}

func (a *App) handleEnableSnapshots(cameraID string, enabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.client.SetSnapshots(ctx, cameraID, enabled); err != nil {
		log.Printf("plugin-frigate: failed to set snapshots for %s: %v", cameraID, err)
		a.updateCameraState(cameraID, func(s *CameraState) {
			s.LastError = err.Error()
		})
		return
	}

	log.Printf("plugin-frigate: snapshots %s for camera %s",
		map[bool]string{true: "enabled", false: "disabled"}[enabled], cameraID)

	a.updateCameraState(cameraID, func(s *CameraState) {
		s.SnapshotsEnabled = enabled
		s.LastError = ""
	})
}

func (a *App) updateCameraState(cameraID string, update func(*CameraState)) {
	eKey := domain.EntityKey{Plugin: PluginID, DeviceID: cameraID, ID: cameraID}
	raw, err := a.store.Get(eKey)
	if err != nil {
		log.Printf("plugin-frigate: failed to get camera %s: %v", cameraID, err)
		return
	}

	var entity domain.Entity
	if err := json.Unmarshal(raw, &entity); err != nil {
		log.Printf("plugin-frigate: failed to unmarshal camera %s: %v", cameraID, err)
		return
	}

	state := ConvertToCameraState(entity.State)
	update(&state)
	entity.State = state

	if err := a.store.Save(entity); err != nil {
		log.Printf("plugin-frigate: failed to update camera state %s: %v", cameraID, err)
	}
}

func ConvertToCameraState(v any) CameraState {
	state := CameraState{}

	switch s := v.(type) {
	case CameraState:
		return s
	case map[string]interface{}:
		if v, ok := s["connected"].(bool); ok {
			state.Connected = v
		}
		if v, ok := s["enabled"].(bool); ok {
			state.Enabled = v
		}
		if v, ok := s["detect_enabled"].(bool); ok {
			state.DetectEnabled = v
		}
		if v, ok := s["record_enabled"].(bool); ok {
			state.RecordEnabled = v
		}
		if v, ok := s["snapshots_enabled"].(bool); ok {
			state.SnapshotsEnabled = v
		}
		if v, ok := s["last_error"].(string); ok {
			state.LastError = v
		}
		if v, ok := s["zones"].([]interface{}); ok {
			state.Zones = make([]string, 0, len(v))
			for _, z := range v {
				if zs, ok := z.(string); ok {
					state.Zones = append(state.Zones, zs)
				}
			}
		}
	default:
		data, _ := json.Marshal(v)
		_ = json.Unmarshal(data, &state)
	}

	return state
}

func (a *App) cameraState(config CameraConfig, agg map[string]labelAgg) CameraState {
	zones := make([]string, 0, len(config.Zones))
	for zoneName := range config.Zones {
		zones = append(zones, zoneName)
	}

	state := CameraState{
		Connected:        true,
		Enabled:          config.Enabled,
		DetectEnabled:    config.Detect.Enabled,
		RecordEnabled:    config.Record.Enabled,
		SnapshotsEnabled: config.Snap.Enabled,
		Zones:            zones,
	}
	for _, labelAgg := range agg {
		if labelAgg.LastEvent != nil && (state.LastEvent == nil || labelAgg.LastEvent.StartTime > state.LastEvent.StartTime) {
			event := *labelAgg.LastEvent
			state.LastEvent = &event
		}
	}
	return state
}

func (a *App) childEntities(camera string, config CameraConfig, agg map[string]labelAgg) []domain.Entity {
	entities := []domain.Entity{
		{
			ID:       "availability",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_availability",
			Name:     "Availability",
			State:    AvailabilityState{Available: true},
		},
		{
			ID:       "image-latest",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_image",
			Name:     "Latest Snapshot",
			State: ImageState{
				URL:    a.apiURL(fmt.Sprintf("/api/%s/latest.jpg", camera)),
				Format: "jpeg",
				Online: true,
			},
		},
	}

	for _, spec := range streamSpecs() {
		entities = append(entities, domain.Entity{
			ID:       spec.ID,
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_stream",
			Name:     spec.Name,
			State: StreamState{
				URL:    a.go2rtcURL(spec.Path, camera),
				Format: spec.Format,
				Kind:   spec.Kind,
				Online: true,
			},
		})
	}

	entities = append(entities, a.eventEntities(camera, agg)...)
	entities = append(entities, a.summaryEntities(camera, agg)...)
	entities = append(entities, a.configEntities(camera, config)...)
	return entities
}

func (a *App) eventEntities(camera string, agg map[string]labelAgg) []domain.Entity {
	labels := make([]string, 0, len(agg))
	for label := range agg {
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		labels = []string{"person", "car"}
	}

	entities := make([]domain.Entity, 0, len(labels))
	for _, label := range labels {
		item := agg[label]
		state := EventSensorState{
			Camera:       camera,
			Label:        label,
			HasSnapshot:  false,
			HasClip:      false,
			EventPresent: false,
		}
		if item.LastEvent != nil {
			state.LastEventID = item.LastEvent.ID
			state.HasSnapshot = item.LastEvent.HasSnapshot
			state.HasClip = item.LastEvent.HasClip
			state.EventPresent = true
			if item.LastEvent.StartTime > 0 {
				state.LastEventAt = time.Unix(int64(item.LastEvent.StartTime), 0).UTC().Format(time.RFC3339)
			}
		}
		entities = append(entities, domain.Entity{
			ID:       "event-" + sanitizeID(label),
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_event_sensor",
			Name:     strings.Title(label) + " Events",
			State:    state,
		})
	}
	return entities
}

func (a *App) summaryEntities(camera string, agg map[string]labelAgg) []domain.Entity {
	allCount := 0
	allActive := 0
	for _, item := range agg {
		allCount += item.Count
		allActive += item.ActiveCount
	}
	occupancy := "Clear"
	if allActive > 0 {
		occupancy = "Detected"
	}

	return []domain.Entity{
		{
			ID:       "status-all-count",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "All Count",
			State: StatusSensorState{
				Value:     fmt.Sprintf("%d objects", allCount),
				Count:     allCount,
				Available: true,
			},
		},
		{
			ID:       "status-all-active-count",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "All Active Count",
			State: StatusSensorState{
				Value:       fmt.Sprintf("%d objects", allActive),
				ActiveCount: allActive,
				Available:   true,
			},
		},
		{
			ID:       "status-all-occupancy",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "All Occupancy",
			State: StatusSensorState{
				Value:     occupancy,
				Occupancy: occupancy,
				Available: true,
			},
		},
	}
}

func (a *App) configEntities(camera string, config CameraConfig) []domain.Entity {
	return []domain.Entity{
		{
			ID:       "status-detect",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "Detect",
			State:    StatusSensorState{Value: onOff(config.Detect.Enabled), Available: true},
		},
		{
			ID:       "status-motion",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "Motion",
			State:    StatusSensorState{Value: onOff(config.Motion.Enabled), Available: true},
		},
		{
			ID:       "status-record",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "Record",
			State:    StatusSensorState{Value: onOff(config.Record.Enabled), Available: true},
		},
		{
			ID:       "status-snapshots",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "Snapshots",
			State:    StatusSensorState{Value: onOff(config.Snap.Enabled), Available: true},
		},
		{
			ID:       "status-review-alerts",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "Review Alerts",
			State:    StatusSensorState{Value: onOff(config.Review.Alerts.Enabled), Available: true},
		},
		{
			ID:       "status-review-detections",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_status_sensor",
			Name:     "Review Detections",
			State:    StatusSensorState{Value: onOff(config.Review.Detections.Enabled), Available: true},
		},
	}
}

func aggregateEventsByCamera(events []Event) map[string]map[string]labelAgg {
	out := make(map[string]map[string]labelAgg)
	for _, event := range events {
		camera := strings.TrimSpace(event.Camera)
		label := strings.ToLower(strings.TrimSpace(event.Label))
		if camera == "" || label == "" {
			continue
		}
		if out[camera] == nil {
			out[camera] = make(map[string]labelAgg)
		}
		item := out[camera][label]
		item.Count++
		if event.EndTime == 0 {
			item.ActiveCount++
		}
		if item.LastEvent == nil || event.StartTime > item.LastEvent.StartTime {
			e := event
			item.LastEvent = &e
		}
		out[camera][label] = item
	}
	return out
}

func streamSpecs() []streamSpec {
	return []streamSpec{
		{ID: "stream-main", Name: "Main Stream", Path: "/stream.html?src=%s", Format: "html", Kind: "main"},
		{ID: "stream-mp4", Name: "MP4 Stream", Path: "/api/stream.mp4?src=%s", Format: "mp4", Kind: "video"},
		{ID: "stream-hls", Name: "HLS Stream", Path: "/api/stream.m3u8?src=%s", Format: "hls", Kind: "video"},
		{ID: "stream-mjpeg", Name: "MJPEG Stream", Path: "/api/stream.mjpeg?src=%s", Format: "mjpeg", Kind: "video"},
	}
}

func (a *App) go2rtcURL(pathFmt, camera string) string {
	base := strings.TrimRight(a.config.Go2RTCURL, "/")
	if base == "" {
		base = strings.TrimRight(a.config.URL, "/")
	}
	return base + fmt.Sprintf(pathFmt, camera)
}

func (a *App) apiURL(path string) string {
	return strings.TrimRight(a.config.URL, "/") + path
}

func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('-')
	}
	return strings.Trim(b.String(), "-")
}

func onOff(v bool) string {
	if v {
		return "On"
	}
	return "Off"
}
