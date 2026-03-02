package consul

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

const (
	kvPrefix = "nexus/config/cb/"

	KeyFailureThreshold = kvPrefix + "webhook_failure_threshold"
	KeySuccessThreshold = kvPrefix + "webhook_success_threshold"
	KeyTimeout          = kvPrefix + "webhook_timeout_seconds"
	KeyOpenDuration     = kvPrefix + "webhook_open_duration_seconds"
)

// CBConfig holds Circuit Breaker configuration loaded from Consul KV.
type CBConfig struct {
	FailureThreshold uint32
	SuccessThreshold uint32
	Timeout          time.Duration
	OpenDuration     time.Duration
}

// DefaultCBConfig returns sensible defaults if Consul KV is empty.
func DefaultCBConfig() CBConfig {
	return CBConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Timeout:          10 * time.Second,
		OpenDuration:     30 * time.Second,
	}
}

// KVWatcher reads Circuit Breaker config from Consul KV and watches for changes.
type KVWatcher struct {
	client *consulapi.Client
	mu     sync.RWMutex
	config CBConfig
}

func NewKVWatcher(consulAddr string) (*KVWatcher, error) {
	w := &KVWatcher{
		config: DefaultCBConfig(),
	}

	cfg := consulapi.DefaultConfig()
	cfg.Address = consulAddr
	client, err := consulapi.NewClient(cfg)
	if err != nil {
		return w, fmt.Errorf("creating consul KV client: %w", err)
	}
	w.client = client

	// Initial load
	w.reload()

	return w, nil
}

// Config returns a snapshot of the current CB config (thread-safe).
func (w *KVWatcher) Config() CBConfig {
	if w == nil {
		return DefaultCBConfig()
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.config
}

// SeedDefaults writes default values to Consul KV if they don't exist yet.
func (w *KVWatcher) SeedDefaults() {
	if w.client == nil {
		return
	}
	kv := w.client.KV()
	defaults := map[string]string{
		KeyFailureThreshold: "5",
		KeySuccessThreshold: "2",
		KeyTimeout:          "10",
		KeyOpenDuration:     "30",
	}

	for key, val := range defaults {
		existing, _, err := kv.Get(key, nil)
		if err != nil {
			slog.Error("failed to check Consul KV key", "key", key, "error", err)
			continue
		}
		if existing == nil {
			_, err := kv.Put(&consulapi.KVPair{Key: key, Value: []byte(val)}, nil)
			if err != nil {
				slog.Error("failed to seed Consul KV default", "key", key, "error", err)
			} else {
				slog.Info("seeded Consul KV default", "key", key, "value", val)
			}
		}
	}
}

// Watch polls Consul KV for config changes. Blocks until ctx is cancelled.
func (w *KVWatcher) Watch(ctx context.Context, interval time.Duration) {
	if w.client == nil {
		slog.Warn("consul KV watcher disabled (no client)")
		return
	}
	slog.Info("consul KV watcher started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("consul KV watcher stopped")
			return
		case <-ticker.C:
			w.reload()
		}
	}
}

func (w *KVWatcher) reload() {
	if w.client == nil {
		return
	}
	kv := w.client.KV()
	newCfg := DefaultCBConfig()

	// Track if we had any errors during reload
	hasErrors := false

	if v, err := w.getUint32(kv, KeyFailureThreshold); err == nil && v > 0 {
		newCfg.FailureThreshold = v
	} else if err != nil {
		hasErrors = true
	}

	if v, err := w.getUint32(kv, KeySuccessThreshold); err == nil && v > 0 {
		newCfg.SuccessThreshold = v
	} else if err != nil {
		hasErrors = true
	}

	if v, err := w.getDuration(kv, KeyTimeout); err == nil && v > 0 {
		newCfg.Timeout = v
	} else if err != nil {
		hasErrors = true
	}

	if v, err := w.getDuration(kv, KeyOpenDuration); err == nil && v > 0 {
		newCfg.OpenDuration = v
	} else if err != nil {
		hasErrors = true
	}

	if hasErrors {
		slog.Warn("partial failure reloading Consul KV config, keeping some defaults/last known")
	}

	w.mu.Lock()
	old := w.config
	w.config = newCfg
	w.mu.Unlock()

	if old != newCfg {
		slog.Info("CB config updated from Consul KV",
			"failure_threshold", newCfg.FailureThreshold,
			"success_threshold", newCfg.SuccessThreshold,
			"timeout", newCfg.Timeout,
			"open_duration", newCfg.OpenDuration,
		)
	}
}

func (w *KVWatcher) getUint32(kv *consulapi.KV, key string) (uint32, error) {
	pair, _, err := kv.Get(key, nil)
	if err != nil {
		return 0, err
	}
	if pair == nil {
		return 0, nil
	}
	n, err := strconv.ParseUint(string(pair.Value), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", key, err)
	}
	return uint32(n), nil
}

func (w *KVWatcher) getDuration(kv *consulapi.KV, key string) (time.Duration, error) {
	pair, _, err := kv.Get(key, nil)
	if err != nil {
		return 0, err
	}
	if pair == nil {
		return 0, nil
	}
	secs, err := strconv.Atoi(string(pair.Value))
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", key, err)
	}
	return time.Duration(secs) * time.Second, nil
}
