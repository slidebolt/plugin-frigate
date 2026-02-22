package device

import (
	"fmt"
	 "github.com/slidebolt/plugin-frigate/pkg/logic"
	"github.com/slidebolt/plugin-sdk"
	"sync"
)

type cameraAdapter struct {
	bundle sdk.Bundle
	name   string
	device sdk.Device
	entity sdk.Entity
}

var (
	adaptersMu sync.Mutex
	adapters   = map[string]*cameraAdapter{}
)

func RegisterCamera(
	b sdk.Bundle,
	name string,
	config logic.CameraConfig,
	stats *logic.FrigateStats,
	streams map[string]logic.RTCStreamInfo,
) {
	sid := sdk.SourceID(name)
	if sid == "" {
		return
	}

	adaptersMu.Lock()
	defer adaptersMu.Unlock()

	key := fmt.Sprintf("%s/%s", b.ID(), sid)
	if a, ok := adapters[key]; ok {
		a.publish(config, stats, streams, true)
		return
	}

	var dev sdk.Device
	if obj, ok := b.GetBySourceID(sid); ok {
		dev = obj.(sdk.Device)
	} else {
		created, err := b.CreateDevice()
		if err != nil {
			return
		}
		dev = created
		_ = dev.UpdateMetadata(name, sid)
	}

	var cam sdk.Entity
	if ents, err := dev.GetEntities(); err == nil && len(ents) > 0 {
		cam = ents[0]
	} else {
		created, err := dev.CreateEntityEx(sdk.TYPE_CAMERA, []string{sdk.CAP_STREAM})
		if err != nil {
			return
		}
		cam = created
		_ = cam.UpdateMetadata("Camera", sdk.SourceID(fmt.Sprintf("%s-camera", name)))
	}

	a := &cameraAdapter{bundle: b, name: name, device: dev, entity: cam}
	adapters[key] = a
	a.publish(config, stats, streams, true)

	b.Log().Info("Registered Frigate Camera: %s", name)
}

func MarkStaleCameras(b sdk.Bundle, active map[string]struct{}) {
	adaptersMu.Lock()
	defer adaptersMu.Unlock()
	for key, a := range adapters {
		if a.bundle.ID() != b.ID() {
			continue
		}
		if _, ok := active[a.name]; ok {
			continue
		}
		a.publish(logic.CameraConfig{}, nil, nil, false)
		delete(adapters, key)
	}
}

func (a *cameraAdapter) publish(config logic.CameraConfig, stats *logic.FrigateStats, streams map[string]logic.RTCStreamInfo, online bool) {
	// 1. Functional State (The "Clean" snapshot)
	props := map[string]interface{}{
		"online": online,
		"name":   a.name,
	}

	// 2. Hardware Technical Data (The "Raw" bucket)
	raw := map[string]interface{}{
		"camera_name": a.name,
		"detecting":   config.Detect.Enabled,
		"recording":   config.Record.Enabled,
		"online":      online,
	}

	if streams != nil {
		if s, ok := streams[a.name]; ok {
			raw["streams"] = s
			if len(s.Producers) > 0 {
				raw["stream_source"] = s.Producers[0].RemoteAddr
				// Stream URL is functional, move to Props
				props["stream_url"] = s.Producers[0].URL
			}
		}
	}

	if stats != nil {
		if s, ok := stats.Cameras[a.name]; ok {
			raw["fps"] = s.CameraFPS
			raw["process_fps"] = s.ProcessFPS
		}
	}

	// Persistence & Communication
	_ = a.entity.UpdateRaw(raw)
	_ = a.device.UpdateRaw(raw)
	
	if online {
		_ = a.entity.UpdateProperties(props)
	} else {
		_ = a.entity.Disable("offline")
	}
}
