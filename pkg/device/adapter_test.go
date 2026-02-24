package device

import (
	"os"
	"github.com/slidebolt/plugin-framework"
	 "github.com/slidebolt/plugin-frigate/pkg/logic"
	"github.com/slidebolt/plugin-sdk"
	"testing"
)

func TestRegisterCamera_PublishesOnlineState(t *testing.T) {
	_ = os.RemoveAll("state")
	_ = os.RemoveAll("logs")
	t.Cleanup(func() {
		_ = os.RemoveAll("state")
		_ = os.RemoveAll("logs")
	})

	b, err := framework.RegisterBundle("plugin-frigate-adapter-test")
	if err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	cfg := logic.CameraConfig{Name: "front", Enabled: true}
	cfg.Detect.Enabled = true
	cfg.Record.Enabled = true
	stats := &logic.FrigateStats{Cameras: map[string]logic.CameraStats{"front": {CameraFPS: 12.3, ProcessFPS: 10.1}}}
	streams := map[string]logic.RTCStreamInfo{
		"front": {Producers: []struct {
			URL        string   `json:"url"`
			RemoteAddr string   `json:"remote_addr"`
			Medias     []string `json:"medias"`
		}{{URL: "webrtc://front", RemoteAddr: "10.0.0.2", Medias: []string{"video"}}}},
	}

	RegisterCamera(b, "front", cfg, stats, streams)

	devObj, ok := b.GetBySourceID(sdk.SourceID("front"))
	if !ok {
		t.Fatalf("expected camera device")
	}
	dev := devObj.(sdk.Device)
	ents, _ := dev.GetEntities()
	if len(ents) == 0 {
		t.Fatalf("expected camera entity")
	}
	cam := ents[0]

	if cam.State().Status != "active" {
		t.Fatalf("expected active status, got %s", cam.State().Status)
	}
	if _, ok := cam.State().Properties["stream_url"]; !ok {
		t.Fatalf("expected stream_url in properties")
	}
}
