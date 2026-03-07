// Package frigate provides a client for the Frigate NVR API
package frigate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Error constants for protocol-level errors
var (
	ErrOffline      = errors.New("device is offline")
	ErrUnauthorized = errors.New("authentication failed")
	ErrTimeout      = errors.New("connection timeout")
	ErrAPIFailure   = errors.New("API request failed")
)

// FrigateConfig represents the Frigate server configuration
type FrigateConfig struct {
	Cameras map[string]CameraConfig `json:"cameras"`
}

// CameraConfig represents a single camera's configuration
type CameraConfig struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Detect  struct {
		Enabled bool `json:"enabled"`
	} `json:"detect"`
	Motion struct {
		Enabled bool `json:"enabled"`
	} `json:"motion"`
	Record struct {
		Enabled bool `json:"enabled"`
	} `json:"record"`
	Snapshots struct {
		Enabled bool `json:"enabled"`
	} `json:"snapshots"`
	Review struct {
		Alerts struct {
			Enabled bool `json:"enabled"`
		} `json:"alerts"`
		Detections struct {
			Enabled bool `json:"enabled"`
		} `json:"detections"`
	} `json:"review"`
	Objects struct {
		Track []string `json:"track"`
	} `json:"objects"`
}

// RTCStreamInfo represents WebRTC stream information
type RTCStreamInfo struct {
	Producers []struct {
		URL        string   `json:"url"`
		RemoteAddr string   `json:"remote_addr"`
		Medias     []string `json:"medias"`
	} `json:"producers"`
}

// FrigateStats represents camera statistics
type FrigateStats struct {
	Cameras map[string]CameraStats `json:"cameras"`
}

// CameraStats represents statistics for a single camera
type CameraStats struct {
	CameraFPS  float64 `json:"camera_fps"`
	ProcessFPS float64 `json:"process_fps"`
}

// FrigateEvent represents a detection event
type FrigateEvent struct {
	ID          string  `json:"id"`
	Camera      string  `json:"camera"`
	Label       string  `json:"label"`
	StartTime   float64 `json:"start_time"`
	EndTime     float64 `json:"end_time"`
	HasSnapshot bool    `json:"has_snapshot"`
	HasClip     bool    `json:"has_clip"`
}

// Client defines the interface for Frigate API operations
type Client interface {
	GetConfig() (*FrigateConfig, error)
	GetStats() (*FrigateStats, error)
	GetRTCStreams() (map[string]RTCStreamInfo, error)
	GetEvents(limit int) ([]FrigateEvent, error)
}

// HTTPClient implements the Client interface using HTTP
type HTTPClient struct {
	FrigateURL string
	RTCURL     string
	client     *http.Client
}

// NewClient creates a new Frigate HTTP client
func NewClient(fURL, rtcURL string) *HTTPClient {
	return &HTTPClient{
		FrigateURL: strings.TrimSuffix(fURL, "/"),
		RTCURL:     strings.TrimSuffix(rtcURL, "/"),
		client:     &http.Client{Timeout: 5 * time.Second},
	}
}

// GetConfig fetches the Frigate configuration
func (c *HTTPClient) GetConfig() (*FrigateConfig, error) {
	var cfg FrigateConfig
	if err := c.get(c.FrigateURL+"/api/config", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetStats fetches camera statistics
func (c *HTTPClient) GetStats() (*FrigateStats, error) {
	var stats FrigateStats
	if err := c.get(c.FrigateURL+"/api/stats", &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// GetRTCStreams fetches WebRTC stream information
func (c *HTTPClient) GetRTCStreams() (map[string]RTCStreamInfo, error) {
	base := c.RTCURL
	if base == "" {
		base = c.FrigateURL
	}
	var streams map[string]RTCStreamInfo
	if err := c.get(base+"/api/streams", &streams); err != nil {
		return nil, err
	}
	return streams, nil
}

// GetEvents fetches detection events
func (c *HTTPClient) GetEvents(limit int) ([]FrigateEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	var events []FrigateEvent
	if err := c.get(fmt.Sprintf("%s/api/events?limit=%d", c.FrigateURL, limit), &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *HTTPClient) get(url string, target any) error {
	resp, err := c.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("frigate api status: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
