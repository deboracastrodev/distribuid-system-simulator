package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_CircuitBreakerDefaults(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 5, cfg.WebhookCBFailureThreshold)
	assert.Equal(t, 2, cfg.WebhookCBSuccessThreshold)
	assert.Equal(t, 30*time.Second, cfg.WebhookCBOpenDuration)
	assert.Equal(t, 10*time.Second, cfg.WebhookTimeout)
}

func TestLoad_CircuitBreakerFromEnv(t *testing.T) {
	t.Setenv("WEBHOOK_CB_FAILURE_THRESHOLD", "3")
	t.Setenv("WEBHOOK_CB_OPEN_DURATION", "1m")
	t.Setenv("WEBHOOK_TIMEOUT", "500ms")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.WebhookCBFailureThreshold)
	assert.Equal(t, time.Minute, cfg.WebhookCBOpenDuration)
	assert.Equal(t, 500*time.Millisecond, cfg.WebhookTimeout)
}

func TestLoad_RejectsInvalidCircuitBreakerSettings(t *testing.T) {
	tests := map[string]string{
		"WEBHOOK_CB_OPEN_DURATION":     "30", // no unit
		"WEBHOOK_TIMEOUT":              "-1s",
		"WEBHOOK_CB_FAILURE_THRESHOLD": "0",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			_, err := Load()
			assert.ErrorContains(t, err, key)
		})
	}
}
