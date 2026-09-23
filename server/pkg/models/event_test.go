package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventEnvelope_Validate(t *testing.T) {
	seq := func(n int) *int { return &n }
	valid := func() EventEnvelope {
		return EventEnvelope{
			EventType: "OrderCreated",
			PlanID:    "plan_1",
			SeqID:     seq(1),
			OrderID:   "550e8400-e29b-41d4-a716-446655440000",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*EventEnvelope)
		wantErr string
	}{
		{"valid sequenced event", func(*EventEnvelope) {}, ""},
		{"abort without seq_id", func(e *EventEnvelope) { e.EventType = EventTypeAbortPlan; e.SeqID = nil }, ""},
		{"missing plan_id", func(e *EventEnvelope) { e.PlanID = "" }, "plan_id is required"},
		{"order_id not a UUID", func(e *EventEnvelope) { e.OrderID = "order-1" }, "order_id must be a UUID"},
		{"unknown event type", func(e *EventEnvelope) { e.EventType = "OrderTeleported" }, "unknown event type"},
		{"missing seq_id", func(e *EventEnvelope) { e.SeqID = nil }, "seq_id is required"},
		{"seq_id below 1", func(e *EventEnvelope) { e.SeqID = seq(0) }, "seq_id must be >= 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := valid()
			tt.mutate(&e)
			err := e.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
