package dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"

	consulkv "github.com/user/nexus-server/internal/consul"
	"github.com/user/nexus-server/internal/db"
)

type OutboxRepository interface {
	FetchUnprocessedOutbox(ctx context.Context, limit int) ([]db.OutboxEntry, error)
	MarkOutboxProcessed(ctx context.Context, id string) error
}

type Dispatcher struct {
	repo         OutboxRepository
	webhookURL   string
	httpClient   *http.Client
	pollInterval time.Duration
	retryMax     int
	retryDelay   time.Duration
	kvWatcher    *consulkv.KVWatcher

	mu           sync.RWMutex
	cb           *gobreaker.CircuitBreaker[*http.Response]
	lastCBConfig consulkv.CBConfig
}

func New(repo OutboxRepository, webhookURL string, pollInterval time.Duration, retryMax int, retryDelay time.Duration, kvWatcher *consulkv.KVWatcher) *Dispatcher {
	d := &Dispatcher{
		repo:         repo,
		webhookURL:   webhookURL,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		pollInterval: pollInterval,
		retryMax:     retryMax,
		retryDelay:   retryDelay,
		kvWatcher:    kvWatcher,
	}

	d.refreshCircuitBreaker()

	return d
}

// refreshCircuitBreaker recreates the CB if immutable settings changed.
func (d *Dispatcher) refreshCircuitBreaker() {
	newCfg := d.kvWatcher.Config()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Only recreate if immutable settings (MaxRequests/Timeout) changed
	if d.cb != nil &&
		d.lastCBConfig.SuccessThreshold == newCfg.SuccessThreshold &&
		d.lastCBConfig.OpenDuration == newCfg.OpenDuration {
		return
	}

	slog.Info("initializing/refreshing circuit breaker",
		"success_threshold", newCfg.SuccessThreshold,
		"open_duration", newCfg.OpenDuration,
	)

	settings := gobreaker.Settings{
		Name:        "webhook-dispatcher",
		MaxRequests: newCfg.SuccessThreshold,
		Timeout:     newCfg.OpenDuration,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// FailureThreshold is truly dynamic as it's checked here
			threshold := d.kvWatcher.Config().FailureThreshold
			return counts.ConsecutiveFailures >= threshold
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Warn("circuit breaker state changed",
				"name", name,
				"from", from.String(),
				"to", to.String(),
			)
		},
	}

	d.cb = gobreaker.NewCircuitBreaker[*http.Response](settings)
	d.lastCBConfig = newCfg
}

func (d *Dispatcher) getCB() *gobreaker.CircuitBreaker[*http.Response] {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cb
}

// Run polls the outbox table and dispatches webhooks. Blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	slog.Info("outbox dispatcher started",
		"poll_interval", d.pollInterval,
		"circuit_breaker", "enabled",
		"webhook_url", d.webhookURL,
	)
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("outbox dispatcher stopped")
			return
		case <-ticker.C:
			d.refreshCircuitBreaker()
			d.poll(ctx)
		}
	}
}

func (d *Dispatcher) poll(ctx context.Context) {
	entries, err := d.repo.FetchUnprocessedOutbox(ctx, 50)
	if err != nil {
		slog.Error("polling outbox failed", "error", err)
		return
	}

	for _, entry := range entries {
		if err := d.dispatch(ctx, entry); err != nil {
			slog.Error("dispatch failed", "outbox_id", entry.ID, "error", err)
			continue
		}
		if err := d.repo.MarkOutboxProcessed(ctx, entry.ID); err != nil {
			slog.Error("marking outbox processed failed", "outbox_id", entry.ID, "error", err)
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, entry db.OutboxEntry) error {
	var lastErr error
	cb := d.getCB()

	for attempt := 1; attempt <= d.retryMax; attempt++ {
		resp, err := cb.Execute(func() (*http.Response, error) {
			return d.doHTTP(ctx, entry)
		})

		if err != nil {
			lastErr = err
			// If circuit breaker is open, fail fast without retrying
			if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
				slog.Warn("circuit breaker open, skipping webhook", "outbox_id", entry.ID)
				return fmt.Errorf("circuit breaker open: %w", err)
			}
			slog.Warn("webhook attempt failed", "attempt", attempt, "max", d.retryMax, "error", err)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.retryDelay * time.Duration(attempt)):
			}
			continue
		}

		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			slog.Info("webhook dispatched", "outbox_id", entry.ID, "status", resp.StatusCode)
			return nil
		}

		lastErr = fmt.Errorf("webhook returned status %d", resp.StatusCode)
		slog.Warn("webhook non-2xx", "attempt", attempt, "status", resp.StatusCode)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.retryDelay * time.Duration(attempt)):
		}
	}

	return fmt.Errorf("webhook failed after %d attempts: %w", d.retryMax, lastErr)
}

func (d *Dispatcher) doHTTP(ctx context.Context, entry db.OutboxEntry) (*http.Response, error) {
	cfg := d.kvWatcher.Config()

	// Use a context with timeout for the individual request instead of modifying the shared client
	reqCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, d.webhookURL, bytes.NewReader(entry.Payload))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Type", entry.EventType)
	req.Header.Set("X-Aggregate-ID", entry.AggregateID)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// Treat 5xx as errors for circuit breaker
	if resp.StatusCode >= 500 {
		resp.Body.Close()
		return nil, fmt.Errorf("server error: status %d", resp.StatusCode)
	}

	return resp, nil
}

