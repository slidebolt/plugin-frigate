package logic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type FrigateConfig struct {
	Cameras map[string]CameraConfig `json:"cameras"`
}

type CameraConfig struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Detect  struct {
		Enabled bool `json:"enabled"`
	} `json:"detect"`
	Record struct {
		Enabled bool `json:"enabled"`
	} `json:"record"`
}

type RTCStreamInfo struct {
	Producers []struct {
		URL        string   `json:"url"`
		RemoteAddr string   `json:"remote_addr"`
		Medias     []string `json:"medias"`
	} `json:"producers"`
}

type FrigateStats struct {
	Cameras map[string]CameraStats `json:"cameras"`
}

type CameraStats struct {
	CameraFPS  float64 `json:"camera_fps"`
	ProcessFPS float64 `json:"process_fps"`
}

type FrigateClient interface {
	GetConfig() (*FrigateConfig, error)
	GetStats() (*FrigateStats, error)
	GetRTCStreams() (map[string]RTCStreamInfo, error)
}

type HttpClient struct {
	FrigateURL string
	RTCURL     string
	client     *http.Client
}

func NewFrigateClient(fURL, rtcURL string) *HttpClient {
	return &HttpClient{
		FrigateURL: strings.TrimSuffix(fURL, "/"),
		RTCURL:     strings.TrimSuffix(rtcURL, "/"),
		client:     &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *HttpClient) GetConfig() (*FrigateConfig, error) {
	var cfg FrigateConfig
	if err := c.get(c.FrigateURL+"/api/config", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *HttpClient) GetStats() (*FrigateStats, error) {
	var stats FrigateStats
	if err := c.get(c.FrigateURL+"/api/stats", &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

func (c *HttpClient) GetRTCStreams() (map[string]RTCStreamInfo, error) {
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

func (c *HttpClient) get(url string, target any) error {
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
