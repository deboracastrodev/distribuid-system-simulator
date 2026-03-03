package consumer

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/user/nexus-server/internal/db"
	"github.com/user/nexus-server/internal/dlq"
	redisc "github.com/user/nexus-server/internal/redis"
	"github.com/user/nexus-server/internal/telemetry"
	"github.com/user/nexus-server/pkg/models"
)

type Consumer struct {
	client    *kgo.Client
	redis     *redisc.Client
	repo      *db.Repository
	dlq       *dlq.Producer
}

func New(brokers []string, topic, group string, redis *redisc.Client, repo *db.Repository, dlqProducer *dlq.Producer) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, err
	}

	return &Consumer{
		client: client,
		redis:  redis,
		repo:   repo,
		dlq:    dlqProducer,
	}, nil
}

func (c *Consumer) Close() {
	c.client.Close()
}

// Run polls Kafka and processes records. Blocks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) {
	slog.Info("kafka consumer started")

	// Start background worker for expired buffers
	go c.bufferExpirationWorker(ctx)

	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			slog.Info("kafka consumer stopping")
			return
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				slog.Error("kafka fetch error", "topic", e.Topic, "partition", e.Partition, "error", e.Err)
			}
		}

		fetches.EachRecord(func(record *kgo.Record) {
			c.handleRecord(ctx, record)
		})

		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
			slog.Error("commit offsets failed", "error", err)
		}
	}
}

func (c *Consumer) bufferExpirationWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkExpiredBuffers(ctx)
		}
	}
}

func (c *Consumer) checkExpiredBuffers(ctx context.Context) {
	keys, err := c.redis.GetBufferKeys(ctx)
	if err != nil {
		slog.Error("failed to scan buffer keys", "error", err)
		return
	}

	for _, key := range keys {
		// If key still exists but has no TTL (or very low), it might be a candidate.
		// However, we set TTL on every ZAdd. If it exists, it's not expired yet.
		// The requirement is to send to DLQ if they stay too long.
		// A better way: if ZCard > 0 and TTL is low, or if we use a separate "last updated" field.
		// For now, let's just drain and DLQ if the buffer is older than the TTL.
		// Actually, Redis handles deletion. We'd need Keyspace Notifications to be perfect.
		// As a compromise: if we find keys, we check their members.
		// If a buffer stays there and doesn't get drained by processAndDrain, it means there's a permanent gap.
		
		// Logic: if the buffer exists, and it's close to expiration (e.g., < 1 min), drain to DLQ.
		ttl, err := c.redis.TTL(ctx, key)
		if err != nil || ttl < 0 {
			continue
		}

		if ttl < 2*time.Minute {
			slog.Warn("buffer close to expiration, draining to DLQ", "key", key)
			members, _ := c.redis.GetBufferMembers(ctx, key)
			for _, m := range members {
				var event models.EventEnvelope
				json.Unmarshal([]byte(m), &event)
				c.dlq.Send(ctx, &event, "buffer timeout: consecutive event never arrived", "BUFFER_TIMEOUT")
			}
			c.redis.DeleteKey(ctx, key)
		}
	}
}

func (c *Consumer) handleRecord(ctx context.Context, record *kgo.Record) {
	// Extract trace context from Kafka headers
	headers := make(map[string]string)
	for _, h := range record.Headers {
		headers[h.Key] = string(h.Value)
	}
	ctx = telemetry.ExtractTraceParent(ctx, headers)

	ctx, span := telemetry.Tracer.Start(ctx, "process-event")
	defer span.End()

	var event models.EventEnvelope
	if err := json.Unmarshal(record.Value, &event); err != nil {
		slog.Error("failed to parse event", "error", err, "offset", record.Offset)
		span.SetAttributes(attribute.String("error", "parse_failure"))
		c.dlq.SendRaw(ctx, record.Value, "failed to parse event JSON", "PARSE_ERROR")
		return
	}

	span.SetAttributes(
		attribute.String("event.type", event.EventType),
		attribute.String("event.plan_id", event.PlanID),
		attribute.String("event.order_id", event.OrderID),
	)

	// Correlate logs with trace
	traceID := span.SpanContext().TraceID().String()
	slog.Info("event received",
		"type", event.EventType,
		"plan_id", event.PlanID,
		"order_id", event.OrderID,
		"offset", record.Offset,
		"trace_id", traceID,
	)

	// Handle ABORT_PLAN tombstone
	if event.EventType == "ABORT_PLAN" {
		span.SetStatus(codes.Error, "plan aborted")
		c.handleAbort(ctx, &event)
		return
	}

	if event.SeqID == nil {
		slog.Error("event missing seq_id", "event_id", event.EventID)
		c.dlq.Send(ctx, &event, "missing seq_id for non-ABORT event", "MISSING_SEQ_ID")
		return
	}

	c.handleSequencedEvent(ctx, &event)
}

func (c *Consumer) handleAbort(ctx context.Context, event *models.EventEnvelope) {
	// 1. Invalidate plan in Redis and clear buffer
	ctx, redisSpan := telemetry.Tracer.Start(ctx, "redis.abort-plan", trace.WithAttributes(
		attribute.String("plan_id", event.PlanID),
	))
	if err := c.redis.AbortPlan(ctx, event.PlanID); err != nil {
		redisSpan.SetStatus(codes.Error, err.Error())
		slog.Error("redis abort failed", "plan_id", event.PlanID, "error", err)
	}
	redisSpan.End()

	// 2. Mark order as aborted in Postgres
	_, dbSpan := telemetry.Tracer.Start(ctx, "postgres.abort-order", trace.WithAttributes(
		attribute.String("order_id", event.OrderID),
	))
	if err := c.repo.AbortOrder(ctx, event); err != nil {
		dbSpan.SetStatus(codes.Error, err.Error())
		slog.Error("db abort failed", "order_id", event.OrderID, "error", err)
	}
	dbSpan.End()

	slog.Info("ABORT_PLAN processed", "plan_id", event.PlanID, "order_id", event.OrderID)
}

func (c *Consumer) handleSequencedEvent(ctx context.Context, event *models.EventEnvelope) {
	seqID := *event.SeqID

	// Child span for Redis Lua idempotency check
	ctx, luaSpan := telemetry.Tracer.Start(ctx, "redis.check-and-set-seq", trace.WithAttributes(
		attribute.String("plan_id", event.PlanID),
		attribute.Int("seq_id", seqID),
	))
	result, err := c.redis.CheckAndSetSeq(ctx, event.PlanID, seqID)
	if err != nil {
		luaSpan.SetStatus(codes.Error, err.Error())
		luaSpan.End()
		slog.Error("redis check-and-set failed", "error", err)
		c.dlq.Send(ctx, event, "redis check-and-set error: "+err.Error(), "REDIS_ERROR")
		return
	}
	luaSpan.SetAttributes(attribute.String("result", result))
	luaSpan.End()

	switch result {
	case "OK":
		c.processAndDrain(ctx, event)

	case "DUPLICATE":
		slog.Info("duplicate event ignored", "plan_id", event.PlanID, "seq_id", seqID)

	case "OUT_OF_ORDER":
		slog.Info("out-of-order event, buffering", "plan_id", event.PlanID, "seq_id", seqID)
		raw, _ := json.Marshal(event)
		_, bufSpan := telemetry.Tracer.Start(ctx, "redis.buffer-event", trace.WithAttributes(
			attribute.String("plan_id", event.PlanID),
			attribute.Int("seq_id", seqID),
		))
		if err := c.redis.BufferEvent(ctx, event.PlanID, seqID, raw); err != nil {
			bufSpan.SetStatus(codes.Error, err.Error())
			slog.Error("buffering failed", "error", err)
			c.dlq.Send(ctx, event, "failed to buffer: "+err.Error(), "BUFFER_ERROR")
		}
		bufSpan.End()

	case "ABORTED":
		slog.Info("event for aborted plan, discarding", "plan_id", event.PlanID, "seq_id", seqID)
	}
}

func (c *Consumer) processAndDrain(ctx context.Context, event *models.EventEnvelope) {
	// Process the current event in Postgres
	ctx, dbSpan := telemetry.Tracer.Start(ctx, "postgres.process-event", trace.WithAttributes(
		attribute.String("order_id", event.OrderID),
		attribute.String("event_type", event.EventType),
	))
	if _, err := c.repo.ProcessEvent(ctx, event); err != nil {
		dbSpan.SetStatus(codes.Error, err.Error())
		dbSpan.End()
		slog.Error("db process failed", "error", err, "event_id", event.EventID)
		c.dlq.Send(ctx, event, "db process error: "+err.Error(), "DB_ERROR")
		return
	}
	dbSpan.End()

	// Drain any buffered events that are now consecutive
	nextSeq := *event.SeqID + 1
	ctx, drainSpan := telemetry.Tracer.Start(ctx, "redis.drain-buffer", trace.WithAttributes(
		attribute.String("plan_id", event.PlanID),
		attribute.Int("next_seq", nextSeq),
	))
	buffered, err := c.redis.DrainBuffer(ctx, event.PlanID, nextSeq)
	if err != nil {
		drainSpan.SetStatus(codes.Error, err.Error())
		drainSpan.End()
		slog.Error("drain buffer failed", "error", err)
		return
	}
	drainSpan.SetAttributes(attribute.Int("drained_count", len(buffered)))
	drainSpan.End()

	for _, raw := range buffered {
		var bufferedEvent models.EventEnvelope
		if err := json.Unmarshal(raw, &bufferedEvent); err != nil {
			slog.Error("failed to parse buffered event", "error", err)
			continue
		}

		// Update sequence counter in Redis
		if bufferedEvent.SeqID != nil {
			c.redis.CheckAndSetSeq(ctx, bufferedEvent.PlanID, *bufferedEvent.SeqID)
		}

		_, bufDbSpan := telemetry.Tracer.Start(ctx, "postgres.process-buffered-event", trace.WithAttributes(
			attribute.String("order_id", bufferedEvent.OrderID),
			attribute.String("event_type", bufferedEvent.EventType),
		))
		if _, err := c.repo.ProcessEvent(ctx, &bufferedEvent); err != nil {
			bufDbSpan.SetStatus(codes.Error, err.Error())
			slog.Error("db process buffered failed", "error", err)
			c.dlq.Send(ctx, &bufferedEvent, "db process error: "+err.Error(), "DB_ERROR")
		} else {
			slog.Info("buffered event processed", "plan_id", bufferedEvent.PlanID, "seq_id", *bufferedEvent.SeqID)
		}
		bufDbSpan.End()
	}
}
