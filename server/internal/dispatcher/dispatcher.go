// Package dispatcher delivers outbox entries as webhooks.
//
// Delivery is at-least-once: a webhook can be sent again when the dispatcher
// dies between the HTTP response and recording it, so every request carries
// the outbox entry ID as Idempotency-Key for the receiver to deduplicate.
// Within an order (aggregate) notifications are delivered one at a time and in
// order; a failing order never blocks the others.
package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sony/gobreaker/v2"

	consulkv "github.com/user/nexus-server/internal/consul"
	"github.com/user/nexus-server/internal/db"
)

const bookkeepingTimeout = 5 * time.Second

// OutboxStore is the outbox persistence the dispatcher needs.
type OutboxStore interface {
	ClaimOutbox(ctx context.Context, limit int, lease time.Duration) ([]db.OutboxEntry, error)
	MarkOutboxDelivered(ctx context.Context, id string) error
	MarkOutboxFailed(ctx context.Context, id string, retryIn time.Duration, lastErr string, dead bool) error
	ReleaseOutbox(ctx context.Context, id string) error
}

// CBConfigSource provides circuit breaker settings; *consul.KVWatcher
// implements it and reloads them from Consul KV.
type CBConfigSource interface {
	Config() consulkv.CBConfig
}

type Config struct {
	WebhookURL   string
	PollInterval time.Duration
	// Workers is how many webhooks are sent concurrently (to different orders).
	Workers   int
	BatchSize int
	// MaxAttempts is how many times a retryable failure is tried before the
	// entry is dead-lettered.
	MaxAttempts int
	RetryBase   time.Duration
	RetryMax    time.Duration
	// Lease is how long a claimed entry stays reserved; it must outlast one
	// HTTP request.
	Lease time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 50
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	if c.RetryBase <= 0 {
		c.RetryBase = time.Second
	}
	if c.RetryMax <= 0 {
		c.RetryMax = time.Minute
	}
	if c.Lease <= 0 {
		c.Lease = time.Minute
	}
	return c
}

type Dispatcher struct {
	store      OutboxStore
	cfg        Config
	cbSource   CBConfigSource
	httpClient *http.Client

	mu           sync.RWMutex
	cb           *gobreaker.CircuitBreaker[int]
	lastCBConfig consulkv.CBConfig
}

func New(store OutboxStore, cfg Config, cbSource CBConfigSource) *Dispatcher {
	d := &Dispatcher{
		store:      store,
		cfg:        cfg.withDefaults(),
		cbSource:   cbSource,
		httpClient: &http.Client{},
	}
	d.refreshCircuitBreaker()
	return d
}

// refreshCircuitBreaker recreates the CB if immutable settings changed.
func (d *Dispatcher) refreshCircuitBreaker() {
	newCfg := d.cbSource.Config()

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
			threshold := d.cbSource.Config().FailureThreshold
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

	d.cb = gobreaker.NewCircuitBreaker[int](settings)
	d.lastCBConfig = newCfg
}

func (d *Dispatcher) getCB() *gobreaker.CircuitBreaker[int] {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cb
}

// Run delivers outbox entries until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	slog.Info("outbox dispatcher started",
		"webhook_url", d.cfg.WebhookURL,
		"poll_interval", d.cfg.PollInterval,
		"workers", d.cfg.Workers,
		"max_attempts", d.cfg.MaxAttempts,
	)

	for {
		d.refreshCircuitBreaker()
		delivered, err := d.dispatchBatch(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("outbox dispatch failed", "error", err)
		}
		if ctx.Err() != nil {
			slog.Info("outbox dispatcher stopped")
			return
		}
		// A delivery can make the next entry of the same order eligible: poll
		// again right away instead of waiting an interval per notification.
		if delivered > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			slog.Info("outbox dispatcher stopped")
			return
		case <-time.After(d.cfg.PollInterval):
		}
	}
}

// dispatchBatch claims a batch of entries and delivers them concurrently.
// Returns how many were delivered.
func (d *Dispatcher) dispatchBatch(ctx context.Context) (int, error) {
	// While the circuit is open nothing would be sent: leave the entries
	// unclaimed instead of claiming and releasing them.
	if d.getCB().State() == gobreaker.StateOpen {
		return 0, nil
	}

	entries, err := d.store.ClaimOutbox(ctx, d.cfg.BatchSize, d.cfg.Lease)
	if err != nil {
		return 0, err
	}

	var delivered atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, d.cfg.Workers)
	for _, entry := range entries {
		sem <- struct{}{}
		wg.Add(1)
		go func(entry db.OutboxEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			if d.deliver(ctx, entry) {
				delivered.Add(1)
			}
		}(entry)
	}
	wg.Wait()
	return int(delivered.Load()), nil
}

// deliver sends one entry and records the outcome. Returns true if delivered.
func (d *Dispatcher) deliver(ctx context.Context, entry db.OutboxEntry) bool {
	// Record the outcome even if ctx is cancelled mid-request, so a completed
	// delivery is not sent again after a restart.
	bookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()
	log := slog.With("outbox_id", entry.ID, "aggregate_id", entry.AggregateID, "event_type", entry.EventType)

	status, err := d.getCB().Execute(func() (int, error) {
		status, err := d.post(ctx, entry)
		if err != nil {
			return 0, err
		}
		if retryableStatus(status) {
			return status, fmt.Errorf("webhook returned status %d", status)
		}
		// Any other status means the receiver is up: it counts as a success
		// for the circuit breaker even when it rejects the notification.
		return status, nil
	})

	attempt := entry.Attempts + 1
	switch {
	case err == nil && status >= 200 && status < 300:
		// Checked first: a delivery completed right before shutdown must be
		// recorded, not given back and sent again.
		if err := d.store.MarkOutboxDelivered(bookCtx, entry.ID); err != nil {
			log.Error("recording webhook delivery failed", "error", err)
			return false
		}
		log.Info("webhook delivered", "status", status, "attempt", attempt)
		return true

	case errors.Is(err, gobreaker.ErrOpenState), errors.Is(err, gobreaker.ErrTooManyRequests), err != nil && ctx.Err() != nil:
		// Not attempted, or interrupted by shutdown: give it back untouched.
		if err := d.store.ReleaseOutbox(bookCtx, entry.ID); err != nil {
			log.Error("releasing outbox entry failed", "error", err)
		}
		return false

	case err != nil:
		dead := attempt >= d.cfg.MaxAttempts
		retryIn := backoff(attempt, d.cfg.RetryBase, d.cfg.RetryMax)
		if dead {
			log.Error("webhook dead-lettered after max attempts", "attempts", attempt, "error", err)
		} else {
			log.Warn("webhook failed, retry scheduled", "attempt", attempt, "retry_in", retryIn.String(), "error", err)
		}
		if err := d.store.MarkOutboxFailed(bookCtx, entry.ID, retryIn, err.Error(), dead); err != nil {
			log.Error("recording webhook failure failed", "error", err)
		}
		return false

	default:
		// The receiver rejected this notification: retrying cannot help.
		log.Error("webhook rejected, dead-lettered", "status", status)
		reason := fmt.Sprintf("rejected with status %d", status)
		if err := d.store.MarkOutboxFailed(bookCtx, entry.ID, 0, reason, true); err != nil {
			log.Error("recording webhook rejection failed", "error", err)
		}
		return false
	}
}

func (d *Dispatcher) post(ctx context.Context, entry db.OutboxEntry) (int, error) {
	// The request must finish well within the lease, or another dispatcher
	// could claim the entry while it is still in flight.
	timeout := d.cfg.Lease / 2
	if t := d.cbSource.Config().Timeout; t > 0 && t < timeout {
		timeout = t
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, d.cfg.WebhookURL, bytes.NewReader(entry.Payload))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", entry.ID)
	req.Header.Set("X-Event-Type", entry.EventType)
	req.Header.Set("X-Aggregate-ID", entry.AggregateID)
	req.Header.Set("X-Outbox-Position", strconv.FormatInt(entry.Position, 10))
	req.Header.Set("X-Delivery-Attempt", strconv.Itoa(entry.Attempts+1))

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // lets the connection be reused
	return resp.StatusCode, nil
}

// retryableStatus reports whether a response means "try again later": the
// receiver failed (5xx), timed out (408) or asked to slow down (429).
func retryableStatus(status int) bool {
	return status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

// backoff returns the wait before retrying after the given failed attempt:
// base, 2*base, 4*base, ... capped at maxDelay.
func backoff(attempt int, base, maxDelay time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	return min(d, maxDelay)
}
