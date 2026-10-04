// Package config reads the service configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable. Zero values are never used: Load fills defaults.
type Config struct {
	Port            int
	Rate            float64 // tokens refilled per second, per key
	Burst           int     // bucket size, per key
	MaxKeys         int     // distinct keys tracked at once
	CleanupInterval time.Duration
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
	Version         string
}

// Load builds a Config from getenv (usually os.Getenv). defaultVersion is used
// when APP_VERSION is unset.
func Load(getenv func(string) string, defaultVersion string) (Config, error) {
	c := Config{
		Port:            8080,
		Rate:            5,
		Burst:           10,
		MaxKeys:         100_000,
		CleanupInterval: 30 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        slog.LevelInfo,
		Version:         defaultVersion,
	}

	var errs []string
	fail := func(name, value, want string) {
		errs = append(errs, fmt.Sprintf("%s=%q: %s", name, value, want))
	}

	if v := getenv("PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			fail("PORT", v, "must be an integer from 1 to 65535")
		} else {
			c.Port = n
		}
	}
	if v := getenv("RATE_PER_SEC"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || !(f > 0) || math.IsInf(f, 0) {
			fail("RATE_PER_SEC", v, "must be a positive number")
		} else {
			c.Rate = f
		}
	}
	if v := getenv("BURST"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail("BURST", v, "must be an integer >= 1")
		} else {
			c.Burst = n
		}
	}
	if v := getenv("MAX_KEYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail("MAX_KEYS", v, "must be an integer >= 1")
		} else {
			c.MaxKeys = n
		}
	}
	if v := getenv("CLEANUP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			fail("CLEANUP_INTERVAL", v, `must be a positive duration like "30s"`)
		} else {
			c.CleanupInterval = d
		}
	}
	if v := getenv("SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			fail("SHUTDOWN_TIMEOUT", v, `must be a positive duration like "10s"`)
		} else {
			c.ShutdownTimeout = d
		}
	}
	if v := getenv("LOG_LEVEL"); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(strings.ToUpper(v))); err != nil {
			fail("LOG_LEVEL", v, "must be debug, info, warn or error")
		}
	}
	if v := getenv("APP_VERSION"); v != "" {
		c.Version = v
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	return c, nil
}
