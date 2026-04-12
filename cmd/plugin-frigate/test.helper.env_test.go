//go:build integration || local

package main

import (
	"os"
	"strings"
	"testing"
)

func loadEnvLocal(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("../../.env.local")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			t.Setenv(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
}
