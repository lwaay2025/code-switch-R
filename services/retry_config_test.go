package services

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBlacklistConfig(t *testing.T, homeDir string, json string) {
	t.Helper()
	configDir := filepath.Join(homeDir, ".code-switch")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "blacklist-config.json"), []byte(json), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestGetRetryConfig_MaxRetryPerProvider_Explicit(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("USERPROFILE", homeDir)

	writeBlacklistConfig(t, homeDir, `{
  "failureThreshold": 5,
  "maxRetryPerProvider": 2,
  "retryWaitSeconds": 1,
  "dedupeWindowSeconds": 2
}`)

	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)
	retryConfig := blacklistService.GetRetryConfig()

	if retryConfig.FailureThreshold != 5 {
		t.Fatalf("FailureThreshold = %d, want %d", retryConfig.FailureThreshold, 5)
	}
	if retryConfig.MaxRetryPerProvider != 2 {
		t.Fatalf("MaxRetryPerProvider = %d, want %d", retryConfig.MaxRetryPerProvider, 2)
	}
}

func TestGetRetryConfig_MaxRetryPerProvider_InheritFailureThreshold(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("USERPROFILE", homeDir)

	writeBlacklistConfig(t, homeDir, `{
  "failureThreshold": 5,
  "maxRetryPerProvider": 0,
  "retryWaitSeconds": 1,
  "dedupeWindowSeconds": 2
}`)

	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)
	retryConfig := blacklistService.GetRetryConfig()

	if retryConfig.FailureThreshold != 5 {
		t.Fatalf("FailureThreshold = %d, want %d", retryConfig.FailureThreshold, 5)
	}
	if retryConfig.MaxRetryPerProvider != 5 {
		t.Fatalf("MaxRetryPerProvider = %d, want %d", retryConfig.MaxRetryPerProvider, 5)
	}
}

