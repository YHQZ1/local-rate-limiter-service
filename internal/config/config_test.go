package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8080 || c.Rate != 5 || c.Burst != 10 || c.Version != "dev" ||
		c.LogLevel != slog.LevelInfo || c.CleanupInterval != 30*time.Second {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PORT": "9000", "RATE_PER_SEC": "2.5", "BURST": "20", "MAX_KEYS": "50",
		"CLEANUP_INTERVAL": "5s", "SHUTDOWN_TIMEOUT": "1m", "LOG_LEVEL": "debug",
		"APP_VERSION": "v2",
	}), "dev")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Port: 9000, Rate: 2.5, Burst: 20, MaxKeys: 50,
		CleanupInterval: 5 * time.Second, ShutdownTimeout: time.Minute,
		LogLevel: slog.LevelDebug, Version: "v2",
	}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestInvalidValuesAreAllReported(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PORT": "99999", "RATE_PER_SEC": "0", "BURST": "x", "LOG_LEVEL": "loud",
		"CLEANUP_INTERVAL": "-1s",
	}), "dev")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"PORT", "RATE_PER_SEC", "BURST", "LOG_LEVEL", "CLEANUP_INTERVAL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}
