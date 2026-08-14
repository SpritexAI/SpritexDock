package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{
		"SPRITEXDOCK_LISTEN",
		"SPRITEXDOCK_PUBLIC_IP",
		"SPRITEXDOCK_DATA_DIR",
		"SPRITEXDOCK_DOCKER_ENDPOINT",
		"SPRITEXDOCK_CADDY_ADMIN",
		"SPRITEXDOCK_BUILD_MEM_LIMIT",
		"SPRITEXDOCK_BUILD_CPU_LIMIT",
		"SPRITEXDOCK_BUILD_TIMEOUT",
		"SPRITEXDOCK_LOG_RETENTION",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q, want default", cfg.Listen)
	}
	if cfg.BuildMemLimit != 512*1024*1024 {
		t.Errorf("BuildMemLimit = %d, want 512 MiB", cfg.BuildMemLimit)
	}
	if cfg.BuildTimeout != 15*60*1e9 {
		t.Errorf("BuildTimeout = %s, want 15m", cfg.BuildTimeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("SPRITEXDOCK_LISTEN", "0.0.0.0:9000")
	t.Setenv("SPRITEXDOCK_BUILD_MEM_LIMIT", "1024")
	t.Setenv("SPRITEXDOCK_BUILD_CPU_LIMIT", "2.5")
	t.Setenv("SPRITEXDOCK_BUILD_TIMEOUT", "2m")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:9000" || cfg.BuildMemLimit != 1024 || cfg.BuildCPULimit != 2.5 || cfg.BuildTimeout.String() != "2m0s" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Setenv("SPRITEXDOCK_BUILD_TIMEOUT", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted invalid build timeout")
	}

	t.Setenv("SPRITEXDOCK_BUILD_TIMEOUT", "15m")
	t.Setenv("SPRITEXDOCK_BUILD_CPU_LIMIT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted zero CPU limit")
	}
}
