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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/user/nexus-server/internal/config"
	"github.com/user/nexus-server/internal/consumer"
	"github.com/user/nexus-server/internal/db"
	"github.com/user/nexus-server/internal/dispatcher"
	"github.com/user/nexus-server/internal/dlq"
	"github.com/user/nexus-server/internal/metrics"
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

	// --- Postgres ---
	repo, err := db.New(ctx, cfg.PostgresDSN)
	if err != nil {
		slog.Error("postgres init failed", "error", err)
		os.Exit(1)
	}

	// --- Metrics ---
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := metrics.New(registry)
	if err := metrics.RegisterBacklog(registry, repo, 2*time.Second); err != nil {
		slog.Error("registering backlog metrics failed", "error", err)
		os.Exit(1)
	}

	// --- DLQ Producer ---
	dlqProducer, err := dlq.New(cfg.KafkaBrokers, cfg.KafkaDLQTopic)
	if err != nil {
		slog.Error("DLQ producer init failed", "error", err)
		os.Exit(1)
	}

	// --- Kafka Consumer ---
	cons, err := consumer.New(cfg.KafkaBrokers, cfg.KafkaTopic, cfg.KafkaConsumerGroup, cfg.PendingEventTTL, repo, dlqProducer, m)
	if err != nil {
		slog.Error("kafka consumer init failed", "error", err)
		os.Exit(1)
	}

	// --- Outbox Dispatcher (with Circuit Breaker) ---
	disp := dispatcher.New(repo, dispatcher.Config{
		WebhookURL:   cfg.WebhookURL,
		PollInterval: cfg.OutboxPollInterval,
		Workers:      cfg.WebhookWorkers,
		MaxAttempts:  cfg.WebhookMaxAttempts,
		RetryBase:    cfg.WebhookRetryBase,
		Breaker: dispatcher.Breaker{
			FailureThreshold: uint32(cfg.WebhookCBFailureThreshold),
			SuccessThreshold: uint32(cfg.WebhookCBSuccessThreshold),
			OpenDuration:     cfg.WebhookCBOpenDuration,
			RequestTimeout:   cfg.WebhookTimeout,
		},
	}, m)

	// --- HTTP server: /health and /metrics ---
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := repo.Ping(r.Context()); err != nil {
			http.Error(w, "postgres unhealthy", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "OK")
	})
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	httpServer := &http.Server{Addr: fmt.Sprintf(":%d", cfg.ServicePort), Handler: mux}

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
		slog.Info("http server listening (health, metrics)", "port", cfg.ServicePort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
		}
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
	repo.Close()

	if shutdownTracer != nil {
		shutdownTracer(shutdownCtx)
	}

	slog.Info("nexus-server stopped gracefully")
}
