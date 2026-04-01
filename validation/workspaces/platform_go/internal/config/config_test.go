package config

import "testing"

func TestFromEnvDefaultsToStableQuota(t *testing.T) {
	t.Setenv("ACCOUNT_DEFAULT_QUOTA", "")
	cfg := FromEnv()
	if cfg.DefaultQuota != 1000 {
		t.Fatalf("expected default quota 1000, got %d", cfg.DefaultQuota)
	}
}

func TestFromEnvSupportsSmallRollout(t *testing.T) {
	t.Setenv("ACCOUNT_DEFAULT_QUOTA", "small")
	cfg := FromEnv()
	if cfg.DefaultQuota != 250 {
		t.Fatalf("expected small rollout quota 250, got %d", cfg.DefaultQuota)
	}
}
