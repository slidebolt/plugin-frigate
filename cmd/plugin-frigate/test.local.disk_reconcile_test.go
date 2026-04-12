//go:build local

package main

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/slidebolt/plugin-frigate/app"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
	server "github.com/slidebolt/sb-storage-server"
)

func TestSystemMethod_PhysicalDiscovery_DiskBackedReconcileLoop(t *testing.T) {
	loadEnvLocal(t)
	url := os.Getenv("FRIGATE_URL")
	if url == "" {
		t.Skip("FRIGATE_URL not set in .env.local - skipping physical test")
	}
	t.Setenv("FRIGATE_MQTT_HOST", "")
	t.Setenv("FRIGATE_MQTT_PORT", "")
	t.Setenv("FRIGATE_MQTT_USER", "")
	t.Setenv("FRIGATE_MQTT_PASSWORD", "")
	t.Setenv("FRIGATE_MQTT_TOPIC_PREFIX", "")

	msg, payload, err := messenger.MockWithPayload()
	if err != nil {
		t.Fatalf("mock messenger: %v", err)
	}
	defer msg.Close()

	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	prevSlog := slog.Default()
	log.SetOutput(&buf)
	log.SetFlags(0)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		slog.SetDefault(prevSlog)
	}()

	dataDir := t.TempDir()
	serverMsg, err := messenger.Connect(map[string]json.RawMessage{"messenger": payload})
	if err != nil {
		t.Fatalf("storage messenger: %v", err)
	}
	defer serverMsg.Close()

	handler, err := server.NewHandlerWithDir(dataDir)
	if err != nil {
		t.Fatalf("storage handler: %v", err)
	}
	defer handler.Close()

	if n, err := handler.LoadFromDir(); err != nil {
		log.Printf("storage: no existing data: %v", err)
	} else {
		log.Printf("storage: loaded %d entries from disk", n)
	}
	if err := handler.StartWatcher(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	if err := handler.Register(serverMsg); err != nil {
		t.Fatalf("register storage handler: %v", err)
	}
	if err := serverMsg.Flush(); err != nil {
		t.Fatalf("flush storage handler: %v", err)
	}
	log.Printf("storage: ready, serving on storage.>")

	store := storage.ClientFrom(msg)

	p := app.New()
	if _, err := p.OnStart(map[string]json.RawMessage{"messenger": payload}); err != nil {
		t.Fatalf("plugin start failed: %v", err)
	}
	defer p.OnShutdown()

	t.Logf("Running physical Frigate against disk-backed storage for 65s: %s", url)

	initialDeadline := time.Now().Add(20 * time.Second)
	for {
		entries, err := store.Query(storage.Query{
			Where: []storage.Filter{{Field: "plugin", Op: storage.Eq, Value: "plugin-frigate"}},
		})
		if err == nil && len(entries) > 0 {
			break
		}
		if time.Now().After(initialDeadline) {
			t.Fatalf("timed out waiting for initial Frigate discovery, logs:\n%s", buf.String())
		}
		time.Sleep(500 * time.Millisecond)
	}

	time.Sleep(10 * time.Second)
	early := buf.String()
	earlySyncs := strings.Count(early, `msg="storage: file synced from disk" key=plugin-frigate.`)
	earlyRegisters := strings.Count(early, "plugin-frigate: registered camera ")

	time.Sleep(55 * time.Second)
	full := buf.String()
	lateSyncs := strings.Count(full, `msg="storage: file synced from disk" key=plugin-frigate.`)
	lateRegisters := strings.Count(full, "plugin-frigate: registered camera ")

	t.Logf("Frigate startup stability counts: registered_camera early=%d late=%d, disk_syncs early=%d late=%d", earlyRegisters, lateRegisters, earlySyncs, lateSyncs)

	if lateRegisters != earlyRegisters {
		t.Fatalf("expected no additional camera registration passes without MQTT over 65s, early=%d late=%d\nlogs:\n%s", earlyRegisters, lateRegisters, full)
	}
	if lateSyncs != earlySyncs {
		t.Fatalf("expected no additional disk syncs without MQTT over 65s, early=%d late=%d\nlogs:\n%s", earlySyncs, lateSyncs, full)
	}
}
