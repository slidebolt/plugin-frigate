package bundle

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/slidebolt/plugin-frigate/pkg/device"
	"github.com/slidebolt/plugin-frigate/pkg/logic"
	"github.com/slidebolt/plugin-sdk"
)

type FrigatePlugin struct {
	bundle sdk.Bundle
	cancel context.CancelFunc
	wait   func()
}

func (p *FrigatePlugin) Init(b sdk.Bundle) error {
	p.bundle = b
	b.UpdateMetadata("Frigate Video")

	b.OnConfigure(func() {
		p.start()
	})

	p.start()
	return nil
}

func (p *FrigatePlugin) Shutdown() {
	if p.cancel != nil {
		p.cancel()
		p.wait()
	}
}

func (p *FrigatePlugin) start() {
	raw := p.bundle.Raw()

	fURL, _ := raw["frigate_url"].(string)
	if fURL == "" {
		fURL = os.Getenv("FRIGATE_URL")
	}

	rtcURL, _ := raw["frigate_rtc_url"].(string)
	if rtcURL == "" {
		rtcURL = os.Getenv("FRIGATE_RTC_URL")
	}

	if fURL == "" {
		p.bundle.Log().Error("FRIGATE_URL missing in Config or ENV")
		return
	}

	if p.cancel != nil {
		p.cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	var wg sync.WaitGroup
	p.wait = wg.Wait

	client := logic.NewFrigateClient(fURL, rtcURL)
	p.bundle.Log().Info("Frigate Plugin Initializing with %s...", fURL)
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.runDiscovery(ctx, client)
	}()
}

func (p *FrigatePlugin) runDiscovery(ctx context.Context, client logic.FrigateClient) {
	for {
		stats, _ := client.GetStats()
		streams, _ := client.GetRTCStreams()
		cfg, err := client.GetConfig()
		if err == nil {
			active := map[string]struct{}{}
			for name, cam := range cfg.Cameras {
				if cam.Enabled {
					active[name] = struct{}{}
					device.RegisterCamera(p.bundle, name, cam, stats, streams)
				}
			}
			device.MarkStaleCameras(p.bundle, active)
		} else {
			p.bundle.Log().Error("Frigate Discovery Failed: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Minute):
		}
	}
}

func NewPlugin() *FrigatePlugin { return &FrigatePlugin{} }
