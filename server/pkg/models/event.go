package models

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const EventTypeAbortPlan = "ABORT_PLAN"

const (
	StatusCompleted = "completed"
	StatusAborted   = "aborted"
)

type EventEnvelope struct {
	EventID       string         `json:"event_id"`
	EventType     string         `json:"event_type"`
	PlanID        string         `json:"plan_id"`
	SeqID         *int           `json:"seq_id,omitempty"`
	OrderID       string         `json:"order_id"`
	Timestamp     string         `json:"timestamp"`
	SchemaVersion string         `json:"schema_version"`
	Data          map[string]any `json:"data"`
}

// Validate rejects envelopes that can never be processed, so they go to the DLQ
// instead of blocking their partition in retries.
func (e *EventEnvelope) Validate() error {
	if e.PlanID == "" {
		return errors.New("plan_id is required")
	}
	if _, err := uuid.Parse(e.OrderID); err != nil {
		return fmt.Errorf("order_id must be a UUID: %q", e.OrderID)
	}
	if e.EventType == EventTypeAbortPlan {
		return nil
	}
	if StatusFromEventType(e.EventType) == "" {
		return fmt.Errorf("unknown event type: %q", e.EventType)
	}
	if e.SeqID == nil {
		return errors.New("seq_id is required for non-ABORT events")
	}
	if *e.SeqID < 1 {
		return fmt.Errorf("seq_id must be >= 1, got %d", *e.SeqID)
	}
	return nil
}

// StatusFromEventType maps event types to order statuses.
func StatusFromEventType(eventType string) string {
	switch eventType {
	case "OrderCreated":
		return "pending"
	case "InventoryValidated":
		return "inventory_validated"
	case "PaymentProcessed":
		return "payment_processed"
	case "OrderShipped":
		return "shipped"
	case "OrderCompleted":
		return StatusCompleted
	case EventTypeAbortPlan:
		return StatusAborted
	default:
		return ""
	}
}

// DLQMessage wraps an event with error metadata for the dead letter queue.
type DLQMessage struct {
	OriginalEvent *EventEnvelope `json:"original_event"`
	ErrorReason   string         `json:"error_reason"`
	ErrorCode     string         `json:"error_code"`
	FailedAt      time.Time      `json:"failed_at"`
}
