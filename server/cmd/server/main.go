package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	consulapi "github.com/hashicorp/consul/api"

	"github.com/user/nexus-server/internal/config"
	consulkv "github.com/user/nexus-server/internal/consul"
	"github.com/user/nexus-server/internal/consumer"
	"github.com/user/nexus-server/internal/db"
	"github.com/user/nexus-server/internal/dispatcher"
	"github.com/user/nexus-server/internal/dlq"
	redisc "github.com/user/nexus-server/internal/redis"
	"github.com/user/nexus-server/internal/telemetry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- OpenTelemetry ---
	shutdownTracer, err := telemetry.Init(ctx, cfg.OTELEndpoint, cfg.OTELServiceName)
	if err != nil {
		slog.Warn("OpenTelemetry init failed (non-fatal)", "error", err)
	}

	// --- Redis (seq cache: optional, Postgres decides when it is down) ---
	redisClient, err := redisc.New(cfg.RedisAddr, cfg.RedisPassword, cfg.LuaScriptPath)
	if err != nil {
		slog.Error("redis init failed", "error", err)
		os.Exit(1)
	}
	if err := redisClient.Ping(ctx); err != nil {
		slog.Warn("redis unavailable at startup, running without seq cache until it recovers", "error", err)
	} else {
		slog.Info("connected to Redis")
	}

	// --- Postgres ---
	repo, err := db.New(ctx, cfg.PostgresDSN)
	if err != nil {
		slog.Error("postgres init failed", "error", err)
		os.Exit(1)
	}

	// --- DLQ Producer ---
	dlqProducer, err := dlq.New(cfg.KafkaBrokers, cfg.KafkaDLQTopic)
	if err != nil {
		slog.Error("DLQ producer init failed", "error", err)
		os.Exit(1)
	}

	// --- Kafka Consumer ---
	cons, err := consumer.New(cfg.KafkaBrokers, cfg.KafkaTopic, cfg.KafkaConsumerGroup, cfg.PendingEventTTL, repo, redisClient, dlqProducer)
	if err != nil {
		slog.Error("kafka consumer init failed", "error", err)
		os.Exit(1)
	}

	// --- Consul KV Watcher (Circuit Breaker config) ---
	kvWatcher, err := consulkv.NewKVWatcher(cfg.ConsulAddr)
	if err != nil {
		slog.Warn("Consul KV watcher init failed, using defaults", "error", err)
	}
	kvWatcher.SeedDefaults()

	// --- Outbox Dispatcher (with Circuit Breaker) ---
	disp := dispatcher.New(repo, cfg.WebhookURL, cfg.OutboxPollInterval, cfg.WebhookRetryMax, cfg.WebhookRetryDelay, kvWatcher)
	// --- Consul Registration ---
	consulClient, serviceID, err := registerConsul(cfg)
	if err != nil {
		slog.Warn("Consul registration failed (non-fatal)", "error", err)
	}

	// --- Health HTTP server ---
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Postgres is required to process events; the Redis cache is not.
		if err := repo.Ping(r.Context()); err != nil {
			http.Error(w, "postgres unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		if err := redisClient.Ping(r.Context()); err != nil {
			fmt.Fprint(w, "OK (degraded: seq cache unavailable)")
			return
		}
		fmt.Fprint(w, "OK")
	})
	httpServer := &http.Server{Addr: fmt.Sprintf(":%d", cfg.ServicePort), Handler: healthMux}

	// --- Start goroutines ---
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		cons.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		disp.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("health server listening", "port", cfg.ServicePort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("health server error", "error", err)
		}
	}()

	// Consul TTL heartbeat
	if consulClient != nil && serviceID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(cfg.HealthCheckTTL / 3)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					consulClient.Agent().PassTTL("service:"+serviceID, "healthy")
				}
			}
		}()
	}

	// Consul KV config watcher (hot-reload CB settings)
	wg.Add(1)
	go func() {
		defer wg.Done()
		kvWatcher.Watch(ctx)
	}()
	slog.Info("nexus-server started", "topic", cfg.KafkaTopic, "group", cfg.KafkaConsumerGroup)

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("shutdown signal received", "signal", sig)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	httpServer.Shutdown(shutdownCtx)

	// Let the workers finish (the consumer commits what it settled) before
	// closing the connections they use.
	wg.Wait()

	cons.Close()
	dlqProducer.Close()
	redisClient.Close()
	repo.Close()

	if consulClient != nil && serviceID != "" {
		consulClient.Agent().ServiceDeregister(serviceID)
		slog.Info("deregistered from Consul")
	}
	if shutdownTracer != nil {
		shutdownTracer(shutdownCtx)
	}

	slog.Info("nexus-server stopped gracefully")
}

func registerConsul(cfg *config.Config) (*consulapi.Client, string, error) {
	consulCfg := consulapi.DefaultConfig()
	consulCfg.Address = cfg.ConsulAddr
	client, err := consulapi.NewClient(consulCfg)
	if err != nil {
		return nil, "", fmt.Errorf("creating consul client: %w", err)
	}

	serviceID := fmt.Sprintf("%s-%d", cfg.ServiceName, os.Getpid())
	registration := &consulapi.AgentServiceRegistration{
		ID:   serviceID,
		Name: cfg.ServiceName,
		Port: cfg.ServicePort,
		Check: &consulapi.AgentServiceCheck{
			TTL:                            cfg.HealthCheckTTL.String(),
			DeregisterCriticalServiceAfter: "1m",
		},
	}

	if err := client.Agent().ServiceRegister(registration); err != nil {
		return nil, "", fmt.Errorf("registering service: %w", err)
	}

	slog.Info("registered in Consul", "service_id", serviceID)
	return client, serviceID, nil
}
