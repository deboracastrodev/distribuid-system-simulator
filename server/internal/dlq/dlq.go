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

// Producer publishes events that cannot be processed to the dead letter queue.
// Every send waits for the broker's acknowledgement: callers commit the source
// offset only after a send succeeds, so a DLQ outage keeps the event in Kafka
// instead of losing it.
type Producer struct {
	client *kgo.Client
	topic  string
}

func New(brokers []string, topic string) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		// Nothing else creates the DLQ topic, and franz-go does not create
		// topics on produce unless asked. Without this, the first dead letter
		// fails with UNKNOWN_TOPIC_OR_PARTITION and, since sends must be
		// acknowledged, blocks the source partition.
		kgo.AllowAutoTopicCreation(),
		// Surface an unreachable broker as an error the consumer can retry and
		// log, instead of blocking the send until shutdown.
		kgo.RecordDeliveryTimeout(10*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("creating DLQ producer: %w", err)
	}
	return &Producer{client: client, topic: topic}, nil
}

func (p *Producer) Close() {
	p.client.Close()
}

// Send publishes a failed event with error metadata and waits for the ack.
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

	r, err := p.client.ProduceSync(ctx, record).First()
	if err != nil {
		return fmt.Errorf("producing to DLQ: %w", err)
	}
	slog.Warn("event sent to DLQ", "event_id", event.EventID, "code", code, "reason", reason, "topic", r.Topic, "offset", r.Offset)
	return nil
}

// SendRaw publishes raw bytes (events that failed to parse) and waits for the ack.
func (p *Producer) SendRaw(ctx context.Context, raw []byte, reason, code string) error {
	record := &kgo.Record{
		Value: raw,
		Headers: []kgo.RecordHeader{
			{Key: "error_reason", Value: []byte(reason)},
			{Key: "error_code", Value: []byte(code)},
		},
	}

	r, err := p.client.ProduceSync(ctx, record).First()
	if err != nil {
		return fmt.Errorf("producing raw to DLQ: %w", err)
	}
	slog.Warn("raw record sent to DLQ", "code", code, "reason", reason, "topic", r.Topic, "offset", r.Offset)
	return nil
}
