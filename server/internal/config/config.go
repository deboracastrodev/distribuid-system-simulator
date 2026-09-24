package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	KafkaBrokers       []string
	KafkaTopic         string
	KafkaDLQTopic      string
	KafkaConsumerGroup string

	PostgresDSN string

	ServicePort int

	OTELEndpoint    string
	OTELServiceName string

	WebhookURL         string
	WebhookWorkers     int
	WebhookMaxAttempts int
	WebhookRetryBase   time.Duration
	OutboxPollInterval time.Duration

	// Circuit breaker in front of the webhook receiver.
	WebhookCBFailureThreshold int
	WebhookCBSuccessThreshold int
	WebhookCBOpenDuration     time.Duration
	WebhookTimeout            time.Duration

	// PendingEventTTL is how long an out-of-order event waits for its
	// predecessor before it is sent to the DLQ.
	PendingEventTTL time.Duration
}

func Load() (*Config, error) {
	// Try to load .env file if it exists
	loadEnv(".env")

	cfg := &Config{
		KafkaBrokers:       []string{envOrDefault("KAFKA_BOOTSTRAP_SERVERS", "kafka:29092")},
		KafkaTopic:         envOrDefault("KAFKA_TOPIC", "orders"),
		KafkaDLQTopic:      envOrDefault("KAFKA_DLQ_TOPIC", "orders-dlq"),
		KafkaConsumerGroup: envOrDefault("KAFKA_CONSUMER_GROUP", "nexus-server-group"),

		PostgresDSN: envOrDefault("POSTGRES_DSN", "postgres://nexus_user:nexus_pass@postgres:5432/nexus_db?sslmode=disable"),

		ServicePort: envOrDefaultInt("SERVICE_PORT", 8080),

		OTELEndpoint:    envOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "jaeger:4317"),
		OTELServiceName: envOrDefault("OTEL_SERVICE_NAME", "nexus-server"),

		WebhookURL:         envOrDefault("WEBHOOK_URL", "http://localhost:9090/webhook"),
		WebhookWorkers:     envOrDefaultInt("WEBHOOK_WORKERS", 4),
		WebhookMaxAttempts: envOrDefaultInt("WEBHOOK_MAX_ATTEMPTS", 10),
		WebhookRetryBase:   1 * time.Second,
		OutboxPollInterval: 2 * time.Second,

		WebhookCBFailureThreshold: envOrDefaultInt("WEBHOOK_CB_FAILURE_THRESHOLD", 5),
		WebhookCBSuccessThreshold: envOrDefaultInt("WEBHOOK_CB_SUCCESS_THRESHOLD", 2),
		WebhookCBOpenDuration:     envOrDefaultDuration("WEBHOOK_CB_OPEN_DURATION", 30*time.Second),
		WebhookTimeout:            envOrDefaultDuration("WEBHOOK_TIMEOUT", 10*time.Second),

		PendingEventTTL: 1 * time.Hour,
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadEnv reads a .env file and sets environment variables if not already set.
func loadEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return // Ignore error if file doesn't exist
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Only set if not already present in environment
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

func (c *Config) validate() error {
	if len(c.KafkaBrokers) == 0 || c.KafkaBrokers[0] == "" {
		return fmt.Errorf("KAFKA_BOOTSTRAP_SERVERS is required")
	}
	if c.KafkaTopic == "" {
		return fmt.Errorf("KAFKA_TOPIC is required")
	}
	if c.PostgresDSN == "" {
		return fmt.Errorf("POSTGRES_DSN is required")
	}
	if c.WebhookCBFailureThreshold < 1 || c.WebhookCBSuccessThreshold < 1 {
		return fmt.Errorf("WEBHOOK_CB_FAILURE_THRESHOLD and WEBHOOK_CB_SUCCESS_THRESHOLD must be >= 1")
	}
	if c.WebhookCBOpenDuration <= 0 || c.WebhookTimeout <= 0 {
		return fmt.Errorf("WEBHOOK_CB_OPEN_DURATION and WEBHOOK_TIMEOUT must be positive durations (e.g. 30s)")
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// envOrDefaultDuration parses a Go duration ("30s", "1m"). An unparsable value
// yields 0, which validate rejects, instead of silently using the default.
func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}
