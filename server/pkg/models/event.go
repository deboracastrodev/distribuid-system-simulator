package models

import "time"

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
		return "completed"
	case "ABORT_PLAN":
		return "aborted"
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
