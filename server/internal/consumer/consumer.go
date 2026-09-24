package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/user/nexus-server/internal/db"
	"github.com/user/nexus-server/internal/metrics"
	"github.com/user/nexus-server/internal/sequencing"
	"github.com/user/nexus-server/internal/telemetry"
	"github.com/user/nexus-server/pkg/models"
)

const (
	retryBaseDelay = 200 * time.Millisecond
	retryMaxDelay  = 10 * time.Second

	pendingSweepInterval = time.Minute
	pendingSweepBatch    = 100

	commitTimeout = 5 * time.Second
)

// Store persists events and is the only source of truth for sequencing.
type Store interface {
	ApplyEvent(ctx context.Context, event *models.EventEnvelope) (db.Result, error)
	AbortPlan(ctx context.Context, event *models.EventEnvelope) (db.Result, error)
	ExpirePending(ctx context.Context, maxAge time.Duration, limit int, sink func(context.Context, *models.EventEnvelope) error) (int, error)
}

// DeadLetter receives events that can never be processed. A nil error means the
// broker acknowledged the event.
type DeadLetter interface {
	Send(ctx context.Context, event *models.EventEnvelope, reason, code string) error
	SendRaw(ctx context.Context, raw []byte, reason, code string) error
}

type Consumer struct {
	client  *kgo.Client
	store   Store
	dlq     DeadLetter
	metrics *metrics.Metrics

	pendingTTL time.Duration
	retryBase  time.Duration
	retryMax   time.Duration
}

func New(brokers []string, topic, group string, pendingTTL time.Duration, store Store, dlq DeadLetter, m *metrics.Metrics) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, err
	}

	c := newConsumer(store, dlq, pendingTTL, m)
	c.client = client
	return c, nil
}

func newConsumer(store Store, dlq DeadLetter, pendingTTL time.Duration, m *metrics.Metrics) *Consumer {
	return &Consumer{
		store:      store,
		dlq:        countingDLQ{next: dlq, metrics: m},
		metrics:    m,
		pendingTTL: pendingTTL,
		retryBase:  retryBaseDelay,
		retryMax:   retryMaxDelay,
	}
}

func (c *Consumer) Close() {
	c.client.Close()
}

// Run polls Kafka and processes records until ctx is cancelled. A record's
// offset is committed only once the record is settled, so a crash or shutdown
// replays unsettled records instead of losing them; the Store makes replays
// harmless.
func (c *Consumer) Run(ctx context.Context) {
	slog.Info("kafka consumer started")

	sweeperDone := make(chan struct{})
	go func() {
		defer close(sweeperDone)
		c.pendingSweeper(ctx)
	}()
	defer func() { <-sweeperDone }()

	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			slog.Info("kafka consumer stopping")
			return
		}

		for _, e := range fetches.Errors() {
			slog.Error("kafka fetch error", "topic", e.Topic, "partition", e.Partition, "error", e.Err)
		}

		var settled []*kgo.Record
		for iter := fetches.RecordIter(); !iter.Done(); {
			record := iter.Next()
			if err := c.processWithRetry(ctx, record); err != nil {
				break // shutting down: leave this record and the rest uncommitted
			}
			settled = append(settled, record)
		}
		c.commit(settled)

		if ctx.Err() != nil {
			slog.Info("kafka consumer stopping")
			return
		}
		c.recordLag(fetches)
	}
}

// recordLag sets each fetched partition's lag once its whole batch is settled:
// the high watermark reported with the fetch minus the next offset to consume.
func (c *Consumer) recordLag(fetches kgo.Fetches) {
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		if len(p.Records) == 0 {
			return
		}
		next := p.Records[len(p.Records)-1].Offset + 1
		c.metrics.ConsumerLag.
			WithLabelValues(p.Topic, strconv.Itoa(int(p.Partition))).
			Set(float64(max(p.HighWatermark-next, 0)))
	})
}

// commit uses its own context so work finished right before shutdown is still
// committed.
func (c *Consumer) commit(records []*kgo.Record) {
	if len(records) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commitTimeout)
	defer cancel()
	if err := c.client.CommitRecords(ctx, records...); err != nil {
		// Not fatal: the records will be redelivered and deduplicated.
		slog.Error("commit offsets failed", "error", err)
	}
}

// processWithRetry retries transient failures in place. Moving past a record
// would let a later event of the same order overtake it, and committing past
// it would lose it. Returns an error only when ctx is cancelled.
func (c *Consumer) processWithRetry(ctx context.Context, record *kgo.Record) error {
	start := time.Now()
	delay := c.retryBase
	for attempt := 1; ; attempt++ {
		err := c.handleRecord(ctx, record)
		if err == nil {
			c.metrics.EventSettleSeconds.Observe(time.Since(start).Seconds())
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.metrics.ConsumerRetries.Inc()

		slog.Warn("transient failure, retrying record",
			"topic", record.Topic, "partition", record.Partition, "offset", record.Offset,
			"attempt", attempt, "retry_in", delay.String(), "error", err,
		)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, c.retryMax)
	}
}

// handleRecord processes one record. A nil error means the record is settled
// (persisted, deliberately skipped, or acknowledged by the DLQ) and its offset
// may be committed. A non-nil error is transient: the record must be retried.
func (c *Consumer) handleRecord(ctx context.Context, record *kgo.Record) error {
	headers := make(map[string]string, len(record.Headers))
	for _, h := range record.Headers {
		headers[h.Key] = string(h.Value)
	}
	ctx = telemetry.ExtractTraceParent(ctx, headers)

	ctx, span := telemetry.Tracer.Start(ctx, "process-event")
	defer span.End()

	var event models.EventEnvelope
	if err := json.Unmarshal(record.Value, &event); err != nil {
		span.SetStatus(codes.Error, "parse failure")
		slog.Error("failed to parse event", "error", err, "offset", record.Offset)
		return c.dlq.SendRaw(ctx, record.Value, "failed to parse event JSON: "+err.Error(), "PARSE_ERROR")
	}

	span.SetAttributes(
		attribute.String("event.type", event.EventType),
		attribute.String("event.plan_id", event.PlanID),
		attribute.String("event.order_id", event.OrderID),
	)
	slog.Info("event received",
		"type", event.EventType,
		"plan_id", event.PlanID,
		"order_id", event.OrderID,
		"offset", record.Offset,
		"trace_id", span.SpanContext().TraceID().String(),
	)

	if err := event.Validate(); err != nil {
		span.SetStatus(codes.Error, "invalid event")
		return c.dlq.Send(ctx, &event, err.Error(), "INVALID_EVENT")
	}

	if event.EventType == models.EventTypeAbortPlan {
		return c.handleAbort(ctx, &event)
	}
	return c.handleSequenced(ctx, &event)
}

func (c *Consumer) handleSequenced(ctx context.Context, event *models.EventEnvelope) error {
	seq := *event.SeqID
	res, err := c.persist(ctx, "postgres.apply-event", event, c.store.ApplyEvent)
	if err != nil {
		return c.handleStoreError(ctx, event, err)
	}
	if res.Outcome == sequencing.PlanMismatch {
		if err := c.dlq.Send(ctx, event, "order belongs to another plan", "PLAN_MISMATCH"); err != nil {
			return err
		}
	}
	// Counted only once settled: a transient failure above is retried and must
	// not count the same event twice.
	c.metrics.EventsProcessed.WithLabelValues("sequenced", outcomeLabel(res.Outcome)).Inc()
	c.metrics.EventsDrained.Add(float64(res.Drained))

	switch res.Outcome {
	case sequencing.Apply:
		slog.Info("event applied", "plan_id", event.PlanID, "seq_id", seq, "drained", res.Drained, "last_seq", res.LastSeq)
	case sequencing.Duplicate:
		slog.Info("duplicate event ignored", "plan_id", event.PlanID, "seq_id", seq)
	case sequencing.Buffer:
		slog.Info("out-of-order event buffered", "plan_id", event.PlanID, "seq_id", seq, "last_seq", res.LastSeq)
	case sequencing.Discard:
		slog.Info("event for aborted plan discarded", "plan_id", event.PlanID, "seq_id", seq)
	}
	return nil
}

func (c *Consumer) handleAbort(ctx context.Context, event *models.EventEnvelope) error {
	res, err := c.persist(ctx, "postgres.abort-plan", event, c.store.AbortPlan)
	if err != nil {
		return c.handleStoreError(ctx, event, err)
	}
	if res.Outcome == sequencing.PlanMismatch {
		if err := c.dlq.Send(ctx, event, "order belongs to another plan", "PLAN_MISMATCH"); err != nil {
			return err
		}
	}
	c.metrics.EventsProcessed.WithLabelValues("abort", outcomeLabel(res.Outcome)).Inc()

	switch res.Outcome {
	case sequencing.Apply, sequencing.Tombstone, sequencing.Duplicate:
		slog.Info("ABORT_PLAN processed", "plan_id", event.PlanID, "order_id", event.OrderID, "outcome", res.Outcome)
	case sequencing.Discard:
		slog.Info("ABORT_PLAN ignored: order already completed", "plan_id", event.PlanID, "order_id", event.OrderID)
	}
	return nil
}

func outcomeLabel(o sequencing.Outcome) string {
	return strings.ToLower(string(o))
}

func (c *Consumer) persist(ctx context.Context, spanName string, event *models.EventEnvelope,
	op func(context.Context, *models.EventEnvelope) (db.Result, error),
) (db.Result, error) {
	ctx, span := telemetry.Tracer.Start(ctx, spanName, trace.WithAttributes(
		attribute.String("order_id", event.OrderID),
		attribute.String("event_type", event.EventType),
	))
	defer span.End()

	res, err := op(ctx, event)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return res, err
	}
	span.SetAttributes(attribute.String("outcome", string(res.Outcome)), attribute.Int("drained", res.Drained))
	return res, nil
}

// handleStoreError sends data the database rejects to the DLQ and reports every
// other failure as transient.
func (c *Consumer) handleStoreError(ctx context.Context, event *models.EventEnvelope, err error) error {
	if db.IsPermanent(err) {
		return c.dlq.Send(ctx, event, "rejected by database: "+err.Error(), "DB_REJECTED")
	}
	return fmt.Errorf("store: %w", err)
}

// pendingSweeper sends events whose predecessor never arrived to the DLQ.
func (c *Consumer) pendingSweeper(ctx context.Context) {
	ticker := time.NewTicker(pendingSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.expirePending(ctx)
		}
	}
}

func (c *Consumer) expirePending(ctx context.Context) {
	sink := func(ctx context.Context, event *models.EventEnvelope) error {
		return c.dlq.Send(ctx, event, "buffer timeout: predecessor event never arrived", "BUFFER_TIMEOUT")
	}
	for {
		n, err := c.store.ExpirePending(ctx, c.pendingTTL, pendingSweepBatch, sink)
		if err != nil {
			slog.Error("expiring pending events failed", "error", err)
			return
		}
		if n > 0 {
			slog.Warn("expired pending events sent to DLQ", "count", n)
		}
		if n < pendingSweepBatch {
			return
		}
	}
}

// countingDLQ counts dead letters by code once the broker acknowledged them.
type countingDLQ struct {
	next    DeadLetter
	metrics *metrics.Metrics
}

func (d countingDLQ) Send(ctx context.Context, event *models.EventEnvelope, reason, code string) error {
	if err := d.next.Send(ctx, event, reason, code); err != nil {
		return err
	}
	d.metrics.DLQMessages.WithLabelValues(code).Inc()
	return nil
}

func (d countingDLQ) SendRaw(ctx context.Context, raw []byte, reason, code string) error {
	if err := d.next.SendRaw(ctx, raw, reason, code); err != nil {
		return err
	}
	d.metrics.DLQMessages.WithLabelValues(code).Inc()
	return nil
}
