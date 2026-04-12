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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	contract "github.com/slidebolt/sb-contract"
	domain "github.com/slidebolt/sb-domain"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

const PluginID = "plugin-frigate"

type FrigateConfig struct {
	URL       string     `json:"url"`
	Go2RTCURL string     `json:"go2rtc_url,omitempty"`
	Username  string     `json:"username,omitempty"`
	Password  string     `json:"password,omitempty"`
	Timeout   int        `json:"timeout_ms,omitempty"`
	MQTT      MQTTConfig `json:"mqtt,omitempty"`
}

type MQTTConfig struct {
	Host        string `json:"host"`
	Port        string `json:"port"`
	User        string `json:"user,omitempty"`
	Password    string `json:"password,omitempty"`
	TopicPrefix string `json:"topic_prefix,omitempty"`
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
	domain.Register("frigate_camera_status", CameraState{})
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

const ReconcileInterval = 10 * time.Minute

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
	msg        messenger.Messenger
	store      storage.Storage
	cmds       *messenger.Commands
	subs       []messenger.Subscription
	config     FrigateConfig
	client     *FrigateClient
	mqttClient mqtt.Client
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	runtime    map[string]*cameraRuntime
	newTicker  func(time.Duration) *time.Ticker
}

type labelRuntime struct {
	Count     int
	Active    map[string]Event
	LastEvent *Event
}

type cameraRuntime struct {
	Labels    map[string]struct{}
	ByLabel   map[string]*labelRuntime
	LastEvent *Event
	LastError string
}

type streamSpec struct {
	ID     string
	Name   string
	Path   string
	Format string
	Kind   string
}

func New() *App {
	return &App{
		runtime:   make(map[string]*cameraRuntime),
		newTicker: time.NewTicker,
	}
}

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
		go a.reconcileCameras(a.ctx)

		// Initialize MQTT if configured
		if a.config.MQTT.Host != "" {
			opts := mqtt.NewClientOptions()
			brokerURL := fmt.Sprintf("tcp://%s:%s", a.config.MQTT.Host, a.config.MQTT.Port)
			if a.config.MQTT.Port == "" {
				brokerURL = fmt.Sprintf("tcp://%s:1883", a.config.MQTT.Host)
			}
			opts.AddBroker(brokerURL)
			if a.config.MQTT.User != "" {
				opts.SetUsername(a.config.MQTT.User)
			}
			if a.config.MQTT.Password != "" {
				opts.SetPassword(a.config.MQTT.Password)
			}
			opts.SetClientID(PluginID + "-" + fmt.Sprintf("%d", time.Now().UnixNano()))

			a.mqttClient = mqtt.NewClient(opts)
			if token := a.mqttClient.Connect(); token.Wait() && token.Error() != nil {
				log.Printf("plugin-frigate: failed to connect to MQTT: %v", token.Error())
			} else {
				topic := a.config.MQTT.TopicPrefix + "/events"
				if token := a.mqttClient.Subscribe(topic, 0, func(client mqtt.Client, msg mqtt.Message) {
					if err := a.HandleMQTTEvent(msg.Payload()); err != nil {
						log.Printf("plugin-frigate: mqtt handle error: %v", err)
					}
				}); token.Wait() && token.Error() != nil {
					log.Printf("plugin-frigate: failed to subscribe to MQTT topic %s: %v", topic, token.Error())
				} else {
					log.Printf("plugin-frigate: listening for real-time events on MQTT topic: %s", topic)
				}
			}
		}
	}

	log.Println("plugin-frigate: started")
	return nil, nil
}

func (a *App) OnShutdown() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.mqttClient != nil && a.mqttClient.IsConnected() {
		a.mqttClient.Disconnect(250)
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
	a.config.MQTT.Host = os.Getenv("FRIGATE_MQTT_HOST")
	a.config.MQTT.Port = os.Getenv("FRIGATE_MQTT_PORT")
	a.config.MQTT.User = os.Getenv("FRIGATE_MQTT_USER")
	a.config.MQTT.Password = os.Getenv("FRIGATE_MQTT_PASSWORD")
	a.config.MQTT.TopicPrefix = os.Getenv("FRIGATE_MQTT_TOPIC_PREFIX")
	if a.config.MQTT.TopicPrefix == "" {
		a.config.MQTT.TopicPrefix = "frigate"
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
	return a.syncCameraConfig(cameras)
}

func (a *App) reconcileCameras(ctx context.Context) {
	ticker := a.newTicker(ReconcileInterval)
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

func (a *App) handleCommand(addr messenger.Address, cmd any) {
	cameraID := addr.DeviceID

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
		a.setRuntimeLastError(cameraID, err.Error())
		a.updateCameraState(cameraID, func(s *CameraState) {
			s.LastError = err.Error()
		})
		return
	}

	log.Printf("plugin-frigate: detection %s for camera %s",
		map[bool]string{true: "enabled", false: "disabled"}[enabled], cameraID)

	a.setRuntimeLastError(cameraID, "")
	a.updateCameraState(cameraID, func(s *CameraState) {
		s.DetectEnabled = enabled
		s.LastError = ""
	})
	a.updateStatusEntity(cameraID, "status-detect", onOff(enabled))
}

func (a *App) handleEnableRecord(cameraID string, enabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.client.SetRecord(ctx, cameraID, enabled); err != nil {
		log.Printf("plugin-frigate: failed to set record for %s: %v", cameraID, err)
		a.setRuntimeLastError(cameraID, err.Error())
		a.updateCameraState(cameraID, func(s *CameraState) {
			s.LastError = err.Error()
		})
		return
	}

	log.Printf("plugin-frigate: recording %s for camera %s",
		map[bool]string{true: "enabled", false: "disabled"}[enabled], cameraID)

	a.setRuntimeLastError(cameraID, "")
	a.updateCameraState(cameraID, func(s *CameraState) {
		s.RecordEnabled = enabled
		s.LastError = ""
	})
	a.updateStatusEntity(cameraID, "status-record", onOff(enabled))
}

func (a *App) handleEnableSnapshots(cameraID string, enabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.client.SetSnapshots(ctx, cameraID, enabled); err != nil {
		log.Printf("plugin-frigate: failed to set snapshots for %s: %v", cameraID, err)
		a.setRuntimeLastError(cameraID, err.Error())
		a.updateCameraState(cameraID, func(s *CameraState) {
			s.LastError = err.Error()
		})
		return
	}

	log.Printf("plugin-frigate: snapshots %s for camera %s",
		map[bool]string{true: "enabled", false: "disabled"}[enabled], cameraID)

	a.setRuntimeLastError(cameraID, "")
	a.updateCameraState(cameraID, func(s *CameraState) {
		s.SnapshotsEnabled = enabled
		s.LastError = ""
	})
	a.updateStatusEntity(cameraID, "status-snapshots", onOff(enabled))
}

func (a *App) updateCameraState(cameraID string, update func(*CameraState)) {
	eKey := domain.EntityKey{Plugin: PluginID, DeviceID: cameraID, ID: "camera-state"}
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

	if _, err := a.saveEntityIfChanged(entity); err != nil {
		log.Printf("plugin-frigate: failed to update camera state %s: %v", cameraID, err)
	}
}

func (a *App) updateStatusEntity(cameraID, entityID, value string) {
	eKey := domain.EntityKey{Plugin: PluginID, DeviceID: cameraID, ID: entityID}
	raw, err := a.store.Get(eKey)
	if err != nil {
		log.Printf("plugin-frigate: failed to get status %s for %s: %v", entityID, cameraID, err)
		return
	}
	var entity domain.Entity
	if err := json.Unmarshal(raw, &entity); err != nil {
		log.Printf("plugin-frigate: failed to unmarshal status %s for %s: %v", entityID, cameraID, err)
		return
	}
	state := StatusSensorState{Value: value, Available: true}
	if current, ok := entity.State.(StatusSensorState); ok {
		state = current
		state.Value = value
	}
	entity.State = state
	if _, err := a.saveEntityIfChanged(entity); err != nil {
		log.Printf("plugin-frigate: failed to update status %s for %s: %v", entityID, cameraID, err)
	}
}

func (a *App) setRuntimeLastError(cameraID, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime == nil {
		a.runtime = make(map[string]*cameraRuntime)
	}
	runtime, ok := a.runtime[cameraID]
	if !ok {
		runtime = &cameraRuntime{
			Labels:  make(map[string]struct{}),
			ByLabel: make(map[string]*labelRuntime),
		}
		a.runtime[cameraID] = runtime
	}
	runtime.LastError = message
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

func (a *App) cameraState(config CameraConfig, runtime *cameraRuntime) CameraState {
	zones := make([]string, 0, len(config.Zones))
	for zoneName := range config.Zones {
		zones = append(zones, zoneName)
	}
	sort.Strings(zones)

	state := CameraState{
		Connected:        true,
		Enabled:          config.Enabled,
		DetectEnabled:    config.Detect.Enabled,
		RecordEnabled:    config.Record.Enabled,
		SnapshotsEnabled: config.Snap.Enabled,
		Zones:            zones,
	}
	if runtime != nil {
		state.LastError = runtime.LastError
		if runtime.LastEvent != nil {
			event := *runtime.LastEvent
			state.LastEvent = &event
		}
	}
	return state
}

func (a *App) childEntities(camera string, config CameraConfig, runtime *cameraRuntime) []domain.Entity {
	entities := []domain.Entity{
		{
			ID:       "camera-state",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "frigate_camera_status",
			Name:     "Camera State",
			State:    a.cameraState(config, runtime),
		},
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

	entities = append(entities, a.eventEntities(camera, runtime)...)
	entities = append(entities, a.summaryEntities(camera, runtime)...)
	entities = append(entities, a.configEntities(camera, config)...)
	entities = append(entities, a.commandEntities(camera)...)
	return entities
}

func (a *App) eventEntities(camera string, runtime *cameraRuntime) []domain.Entity {
	labels := runtimeLabels(runtime)
	entities := make([]domain.Entity, 0, len(labels))
	for _, label := range labels {
		item := runtime.label(label)
		activeCount := len(item.Active)
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
			state.EventPresent = activeCount > 0
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

func (a *App) summaryEntities(camera string, runtime *cameraRuntime) []domain.Entity {
	allCount := 0
	allActive := 0
	if runtime != nil {
		for _, label := range runtimeLabels(runtime) {
			item := runtime.label(label)
			allCount += item.Count
			allActive += len(item.Active)
		}
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

func (a *App) commandEntities(camera string) []domain.Entity {
	return []domain.Entity{
		{
			ID:       "detect-enable",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "button",
			Name:     "Enable Detect",
			Commands: []string{"frigate_camera_enable_detect"},
			State:    domain.Button{},
		},
		{
			ID:       "detect-disable",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "button",
			Name:     "Disable Detect",
			Commands: []string{"frigate_camera_disable_detect"},
			State:    domain.Button{},
		},
		{
			ID:       "record-enable",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "button",
			Name:     "Enable Record",
			Commands: []string{"frigate_camera_enable_record"},
			State:    domain.Button{},
		},
		{
			ID:       "record-disable",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "button",
			Name:     "Disable Record",
			Commands: []string{"frigate_camera_disable_record"},
			State:    domain.Button{},
		},
		{
			ID:       "snapshots-enable",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "button",
			Name:     "Enable Snapshots",
			Commands: []string{"frigate_camera_enable_snapshots"},
			State:    domain.Button{},
		},
		{
			ID:       "snapshots-disable",
			Plugin:   PluginID,
			DeviceID: camera,
			Type:     "button",
			Name:     "Disable Snapshots",
			Commands: []string{"frigate_camera_disable_snapshots"},
			State:    domain.Button{},
		},
	}
}

func configuredLabels(config CameraConfig) []string {
	labels := make([]string, 0, len(config.Objects.Track))
	seen := make(map[string]struct{}, len(config.Objects.Track))
	for _, label := range config.Objects.Track {
		label = strings.ToLower(strings.TrimSpace(label))
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		labels = []string{"car", "person"}
	}
	sort.Strings(labels)
	return labels
}

func runtimeLabels(runtime *cameraRuntime) []string {
	if runtime == nil || len(runtime.Labels) == 0 {
		return []string{"car", "person"}
	}
	labels := make([]string, 0, len(runtime.Labels))
	for label := range runtime.Labels {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

func (r *cameraRuntime) label(label string) *labelRuntime {
	if r.ByLabel == nil {
		r.ByLabel = make(map[string]*labelRuntime)
	}
	if r.Labels == nil {
		r.Labels = make(map[string]struct{})
	}
	if _, ok := r.Labels[label]; !ok {
		r.Labels[label] = struct{}{}
	}
	if existing, ok := r.ByLabel[label]; ok {
		return existing
	}
	state := &labelRuntime{Active: make(map[string]Event)}
	r.ByLabel[label] = state
	return state
}

func (a *App) ensureCameraRuntime(camera string, config CameraConfig) *cameraRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime == nil {
		a.runtime = make(map[string]*cameraRuntime)
	}
	state, ok := a.runtime[camera]
	if !ok {
		state = &cameraRuntime{
			Labels:  make(map[string]struct{}),
			ByLabel: make(map[string]*labelRuntime),
		}
		a.runtime[camera] = state
	}
	for _, label := range configuredLabels(config) {
		state.label(label)
	}
	return cloneRuntime(state)
}

func (a *App) runtimeSnapshot(camera string) *cameraRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime == nil {
		return &cameraRuntime{
			Labels:  map[string]struct{}{"car": {}, "person": {}},
			ByLabel: map[string]*labelRuntime{},
		}
	}
	state, ok := a.runtime[camera]
	if !ok {
		return &cameraRuntime{
			Labels:  map[string]struct{}{"car": {}, "person": {}},
			ByLabel: map[string]*labelRuntime{},
		}
	}
	return cloneRuntime(state)
}

func normalizedJSON(data []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return string(data)
	}
	return buf.String()
}

func (a *App) saveEntityIfChanged(entity domain.Entity) (bool, error) {
	body, err := json.Marshal(entity)
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", entity.Key(), err)
	}
	current, err := a.store.Get(entity)
	if err == nil && len(current) > 0 && normalizedJSON(current) == normalizedJSON(body) {
		return false, nil
	}
	if err := a.store.Save(entity); err != nil {
		return false, err
	}
	return true, nil
}
func (a *App) deleteEntityByKey(key string) error {
	return a.store.Delete(entityKey(key))
}

func (a *App) desiredEntities(camera string, config CameraConfig) []domain.Entity {
	runtime := a.ensureCameraRuntime(camera, config)
	return a.childEntities(camera, config, runtime)
}

func (a *App) syncCameraConfig(cameras map[string]CameraConfig) error {
	desired := make(map[string]domain.Entity)
	cameraNames := make([]string, 0, len(cameras))
	for name := range cameras {
		cameraNames = append(cameraNames, name)
	}
	sort.Strings(cameraNames)
	for _, name := range cameraNames {
		config := cameras[name]
		for _, entity := range a.desiredEntities(name, config) {
			desired[entity.Key()] = entity
		}
	}

	existing, err := a.store.Search(PluginID + ".>")
	if err != nil {
		return fmt.Errorf("search existing frigate entities: %w", err)
	}
	for _, key := range sortedKeys(desired) {
		entity := desired[key]
		changed, err := a.saveEntityIfChanged(entity)
		if err != nil {
			log.Printf("plugin-frigate: failed to save entity %s: %v", key, err)
			continue
		}
		if changed && entity.ID == "camera-state" {
			state := entity.State.(CameraState)
			log.Printf("plugin-frigate: synced camera %s (enabled=%v, detect=%v)",
				entity.DeviceID, state.Enabled, state.DetectEnabled)
		}
	}

	for _, entry := range existing {
		if _, ok := desired[entry.Key]; ok {
			continue
		}
		if err := a.deleteEntityByKey(entry.Key); err != nil {
			log.Printf("plugin-frigate: failed to delete stale entity %s: %v", entry.Key, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]domain.Entity) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type entityKey string

func (k entityKey) Key() string { return string(k) }

func cloneRuntime(src *cameraRuntime) *cameraRuntime {
	if src == nil {
		return nil
	}
	dst := &cameraRuntime{
		Labels:    make(map[string]struct{}, len(src.Labels)),
		ByLabel:   make(map[string]*labelRuntime, len(src.ByLabel)),
		LastError: src.LastError,
	}
	if src.LastEvent != nil {
		e := *src.LastEvent
		dst.LastEvent = &e
	}
	for label := range src.Labels {
		dst.Labels[label] = struct{}{}
	}
	for label, item := range src.ByLabel {
		copied := &labelRuntime{
			Count:  item.Count,
			Active: make(map[string]Event, len(item.Active)),
		}
		if item.LastEvent != nil {
			e := *item.LastEvent
			copied.LastEvent = &e
		}
		for id, event := range item.Active {
			copied.Active[id] = event
		}
		dst.ByLabel[label] = copied
	}
	return dst
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
