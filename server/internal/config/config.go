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

	RedisAddr     string
	RedisPassword string

	PostgresDSN string

	ConsulAddr    string
	ServiceName   string
	ServicePort   int
	HealthCheckTTL time.Duration

	OTELEndpoint    string
	OTELServiceName string

	WebhookURL         string
	WebhookWorkers     int
	WebhookRetryMax    int
	WebhookRetryDelay  time.Duration
	OutboxPollInterval time.Duration

	BufferTTL time.Duration

	LuaScriptPath string
}

func Load() (*Config, error) {
	// Try to load .env file if it exists
	loadEnv(".env")

	cfg := &Config{
		KafkaBrokers:       []string{envOrDefault("KAFKA_BOOTSTRAP_SERVERS", "kafka:29092")},
		KafkaTopic:         envOrDefault("KAFKA_TOPIC", "orders"),
		KafkaDLQTopic:      envOrDefault("KAFKA_DLQ_TOPIC", "orders-dlq"),
		KafkaConsumerGroup: envOrDefault("KAFKA_CONSUMER_GROUP", "nexus-server-group"),

		RedisAddr:     envOrDefault("REDIS_ADDR", "redis:6379"),
		RedisPassword: envOrDefault("REDIS_PASSWORD", "nexus_pass"),

		PostgresDSN: envOrDefault("POSTGRES_DSN", "postgres://nexus_user:nexus_pass@postgres:5432/nexus_db?sslmode=disable"),

		ConsulAddr:    envOrDefault("CONSUL_ADDR", "consul:8500"),
		ServiceName:   envOrDefault("SERVICE_NAME", "nexus-server"),
		ServicePort:   envOrDefaultInt("SERVICE_PORT", 8080),
		HealthCheckTTL: 30 * time.Second,

		OTELEndpoint:    envOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "jaeger:4317"),
		OTELServiceName: envOrDefault("OTEL_SERVICE_NAME", "nexus-server"),

		WebhookURL:         envOrDefault("WEBHOOK_URL", "http://localhost:9090/webhook"),
		WebhookWorkers:     envOrDefaultInt("WEBHOOK_WORKERS", 2),
		WebhookRetryMax:    envOrDefaultInt("WEBHOOK_RETRY_MAX", 3),
		WebhookRetryDelay:  5 * time.Second,
		OutboxPollInterval: 2 * time.Second,

		BufferTTL: 1 * time.Hour,

		LuaScriptPath: envOrDefault("LUA_SCRIPT_PATH", "/app/scripts/lua/check_and_set_seq.lua"),
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
