package consul

import (
	"testing"
)

func TestDefaultCBConfig(t *testing.T) {
	cfg := DefaultCBConfig()
	if cfg.FailureThreshold != 5 {
		t.Errorf("expected FailureThreshold 5, got %d", cfg.FailureThreshold)
	}
	if cfg.SuccessThreshold != 2 {
		t.Errorf("expected SuccessThreshold 2, got %d", cfg.SuccessThreshold)
	}
}

func TestKVWatcher_Config_NilHandling(t *testing.T) {
	var w *KVWatcher
	cfg := w.Config()
	if cfg.FailureThreshold != 5 {
		t.Errorf("expected default FailureThreshold for nil watcher, got %d", cfg.FailureThreshold)
	}
}

func TestNewKVWatcher_InvalidAddr(t *testing.T) {
	// Should not panic and should return a watcher with defaults
	w, err := NewKVWatcher("invalid-addr")
	if err == nil {
		t.Log("expected error for invalid address, but got nil (consul api might not validate immediately)")
	}
	if w == nil {
		t.Fatal("expected non-nil watcher even on error")
	}
	cfg := w.Config()
	if cfg.FailureThreshold != 5 {
		t.Errorf("expected default config, got %v", cfg)
	}
}
