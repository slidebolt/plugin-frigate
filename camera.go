package main

import (
	"encoding/json"
	"time"

	"github.com/slidebolt/sdk-types"
)

const CameraDomain = "camera"

const (
	ActionStream = "stream"
)

type CameraState struct {
	Online     bool    `json:"online"`
	StreamURL  string  `json:"stream_url,omitempty"`
	Detecting  bool    `json:"detecting"`
	Recording  bool    `json:"recording"`
	FPS        float64 `json:"fps,omitempty"`
	ProcessFPS float64 `json:"process_fps,omitempty"`
}

type CameraStore struct {
	entity *types.Entity
}

func BindCamera(entity *types.Entity) CameraStore {
	return CameraStore{entity: entity}
}

func (s CameraStore) SetReported(state CameraState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	s.entity.Data.Reported = b
	s.entity.Data.Effective = b
	s.entity.Data.UpdatedAt = time.Now()
	return nil
}
