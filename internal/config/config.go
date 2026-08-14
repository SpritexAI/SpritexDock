package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config contains validated control-plane settings.
type Config struct {
	Listen         string
	PublicIP       string
	DataDir        string
	DockerEndpoint string
	CaddyAdmin     string
	BuildMemLimit  int64
	BuildCPULimit  float64
	BuildTimeout   time.Duration
	LogRetention   time.Duration
}

// Load reads configuration from SPRITEXDOCK_* environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		Listen:         envOr("SPRITEXDOCK_LISTEN", "127.0.0.1:8080"),
		PublicIP:       os.Getenv("SPRITEXDOCK_PUBLIC_IP"),
		DataDir:        envOr("SPRITEXDOCK_DATA_DIR", "/var/lib/spritexdock"),
		DockerEndpoint: envOr("SPRITEXDOCK_DOCKER_ENDPOINT", "unix:///var/run/docker.sock"),
		CaddyAdmin:     envOr("SPRITEXDOCK_CADDY_ADMIN", "127.0.0.1:2019"),
	}

	var err error
	if cfg.BuildMemLimit, err = parsePositiveInt("SPRITEXDOCK_BUILD_MEM_LIMIT", 512*1024*1024); err != nil {
		return nil, err
	}
	if cfg.BuildCPULimit, err = parsePositiveFloat("SPRITEXDOCK_BUILD_CPU_LIMIT", 1); err != nil {
		return nil, err
	}
	if cfg.BuildTimeout, err = parsePositiveDuration("SPRITEXDOCK_BUILD_TIMEOUT", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.LogRetention, err = parsePositiveDuration("SPRITEXDOCK_LOG_RETENTION", 30*24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.Listen == "" {
		return nil, fmt.Errorf("SPRITEXDOCK_LISTEN must not be empty")
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("SPRITEXDOCK_DATA_DIR must not be empty")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parsePositiveInt(key string, fallback int64) (int64, error) {
	value := envOr(key, strconv.FormatInt(fallback, 10))
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}

func parsePositiveFloat(key string, fallback float64) (float64, error) {
	value := envOr(key, strconv.FormatFloat(fallback, 'f', -1, 64))
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}

func parsePositiveDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := envOr(key, fallback.String())
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}
