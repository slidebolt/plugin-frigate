package app

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	domain "github.com/slidebolt/sb-domain"
	storage "github.com/slidebolt/sb-storage-sdk"
)

// MQTTEventPayload represents the structure sent by Frigate on frigate/events
type MQTTEventPayload struct {
	Type   string `json:"type"`
	Before Event  `json:"before"`
	After  Event  `json:"after"`
}

// HandleMQTTEvent processes a real-time event from Frigate's MQTT stream.
// It deduplicates updates and modifies the relevant camera state.
func (a *App) HandleMQTTEvent(payload []byte) error {
	var mqttEvent MQTTEventPayload
	if err := json.Unmarshal(payload, &mqttEvent); err != nil {
		return fmt.Errorf("failed to unmarshal MQTT event: %w", err)
	}

	event := mqttEvent.After

	if event.Camera == "" {
		return nil
	}

	log.Printf("plugin-frigate: mqtt received %s event %s for %s", mqttEvent.Type, event.ID, event.Camera)

	a.applyMQTTEvent(mqttEvent.Type, event)
	return a.syncRuntimeEntities(event.Camera)
}

// SetStorage is a helper for unit testing to inject the mock store
func (a *App) SetStorage(s storage.Storage) {
	a.store = s
}

func (a *App) applyMQTTEvent(kind string, event Event) {
	camera := strings.TrimSpace(event.Camera)
	label := strings.ToLower(strings.TrimSpace(event.Label))
	if camera == "" || label == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime == nil {
		a.runtime = make(map[string]*cameraRuntime)
	}
	runtime, ok := a.runtime[camera]
	if !ok {
		runtime = &cameraRuntime{
			Labels:  make(map[string]struct{}),
			ByLabel: make(map[string]*labelRuntime),
		}
		a.runtime[camera] = runtime
	}
	item := runtime.label(label)

	switch kind {
	case "new":
		if _, ok := item.Active[event.ID]; !ok {
			item.Count++
		}
		if event.EndTime == 0 {
			item.Active[event.ID] = event
		}
	case "update":
		if _, ok := item.Active[event.ID]; !ok && event.EndTime == 0 {
			item.Count++
		}
		if event.EndTime == 0 {
			item.Active[event.ID] = event
		} else {
			delete(item.Active, event.ID)
		}
	case "end":
		delete(item.Active, event.ID)
	default:
		if event.EndTime == 0 {
			item.Active[event.ID] = event
		}
	}

	if item.LastEvent == nil || event.StartTime > item.LastEvent.StartTime || event.ID == item.LastEvent.ID {
		e := event
		item.LastEvent = &e
	}
	if runtime.LastEvent == nil || event.StartTime > runtime.LastEvent.StartTime || event.ID == runtime.LastEvent.ID {
		e := event
		runtime.LastEvent = &e
	}
}

func (a *App) syncRuntimeEntities(cameraID string) error {
	cameraKey := domain.EntityKey{Plugin: PluginID, DeviceID: cameraID, ID: "camera-state"}
	raw, err := a.store.Get(cameraKey)
	if err != nil {
		return fmt.Errorf("get camera %s: %w", cameraID, err)
	}

	var cameraEntity domain.Entity
	if err := json.Unmarshal(raw, &cameraEntity); err != nil {
		return fmt.Errorf("unmarshal camera %s: %w", cameraID, err)
	}

	state := ConvertToCameraState(cameraEntity.State)
	runtime := a.runtimeSnapshot(cameraID)
	state.LastError = runtime.LastError
	if runtime.LastEvent != nil {
		e := *runtime.LastEvent
		state.LastEvent = &e
	}
	cameraEntity.State = state
	if _, err := a.saveEntityIfChanged(cameraEntity); err != nil {
		return fmt.Errorf("save camera %s: %w", cameraID, err)
	}

	for _, entity := range a.eventEntities(cameraID, runtime) {
		if _, err := a.saveEntityIfChanged(entity); err != nil {
			return fmt.Errorf("save runtime entity %s: %w", entity.Key(), err)
		}
	}
	for _, entity := range a.summaryEntities(cameraID, runtime) {
		if _, err := a.saveEntityIfChanged(entity); err != nil {
			return fmt.Errorf("save runtime entity %s: %w", entity.Key(), err)
		}
	}
	return nil
}
