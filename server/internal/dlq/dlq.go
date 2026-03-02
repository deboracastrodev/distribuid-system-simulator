package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/user/nexus-server/pkg/models"
)

type Producer struct {
	client *kgo.Client
	topic  string
}

func New(brokers []string, topic string) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
	)
	if err != nil {
		return nil, fmt.Errorf("creating DLQ producer: %w", err)
	}
	return &Producer{client: client, topic: topic}, nil
}

func (p *Producer) Close() {
	p.client.Close()
}

// Send publishes a failed event to the dead letter queue with error metadata.
func (p *Producer) Send(ctx context.Context, event *models.EventEnvelope, reason, code string) error {
	msg := models.DLQMessage{
		OriginalEvent: event,
		ErrorReason:   reason,
		ErrorCode:     code,
		FailedAt:      time.Now().UTC(),
	}

	value, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal DLQ message: %w", err)
	}

	record := &kgo.Record{
		Key:   []byte(event.OrderID),
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "error_reason", Value: []byte(reason)},
			{Key: "error_code", Value: []byte(code)},
			{Key: "original_event_id", Value: []byte(event.EventID)},
		},
	}

	p.client.Produce(ctx, record, func(r *kgo.Record, err error) {
		if err != nil {
			slog.Error("failed to produce to DLQ", "error", err, "event_id", event.EventID)
		} else {
			slog.Warn("event sent to DLQ", "event_id", event.EventID, "reason", reason, "topic", r.Topic, "offset", r.Offset)
		}
	})

	return nil
}

// SendRaw publishes raw bytes to DLQ (for events that failed to parse).
func (p *Producer) SendRaw(ctx context.Context, raw []byte, reason, code string) error {
	record := &kgo.Record{
		Value: raw,
		Headers: []kgo.RecordHeader{
			{Key: "error_reason", Value: []byte(reason)},
			{Key: "error_code", Value: []byte(code)},
		},
	}

	p.client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		if err != nil {
			slog.Error("failed to produce raw to DLQ", "error", err)
		}
	})

	return nil
}
