package consumer

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/user/nexus-server/internal/db"
	redisc "github.com/user/nexus-server/internal/redis"
	"github.com/user/nexus-server/pkg/models"
)

// skipIfNoInfra skips the test if REDIS_ADDR or POSTGRES_DSN are not set.
func skipIfNoInfra(t *testing.T) (string, string, string) {
	t.Helper()
	redisAddr := os.Getenv("REDIS_ADDR")
	redisPass := os.Getenv("REDIS_PASSWORD")
	postgresDSN := os.Getenv("POSTGRES_DSN")
	if redisAddr == "" || postgresDSN == "" {
		t.Skip("Skipping integration test: REDIS_ADDR or POSTGRES_DSN not set")
	}
	return redisAddr, redisPass, postgresDSN
}

func setupConsumer(t *testing.T) *Consumer {
	t.Helper()
	redisAddr, redisPass, postgresDSN := skipIfNoInfra(t)
	ctx := context.Background()

	redisClient, err := redisc.New(redisAddr, redisPass, 1*time.Hour, "../../scripts/lua/check_and_set_seq.lua")
	require.NoError(t, err)
	t.Cleanup(func() { redisClient.Close() })

	repo, err := db.New(ctx, postgresDSN)
	require.NoError(t, err)
	t.Cleanup(func() { repo.Close() })

	return &Consumer{
		redis: redisClient,
		repo:  repo,
	}
}

func makeEvent(planID, orderID, eventType string, seqID int) *models.EventEnvelope {
	seq := seqID
	return &models.EventEnvelope{
		EventID:   uuid.New().String(),
		EventType: eventType,
		PlanID:    planID,
		SeqID:     &seq,
		OrderID:   orderID,
		Data:      map[string]any{"user_id": "test-user", "total_amount": 100.0},
	}
}

func TestConsumer_SequencedFlow(t *testing.T) {
	c := setupConsumer(t)
	ctx := context.Background()

	planID := "plan-" + uuid.New().String()
	orderID := uuid.New().String()

	// 1. Process Seq 1 (OK)
	event1 := makeEvent(planID, orderID, "OrderCreated", 1)
	c.handleSequencedEvent(ctx, event1)

	// 2. Process Seq 2 (OK)
	event2 := makeEvent(planID, orderID, "InventoryValidated", 2)
	c.handleSequencedEvent(ctx, event2)

	// 3. Process Duplicate Seq 1 (should be silently ignored)
	c.handleSequencedEvent(ctx, event1)

	// 4. Process Out-of-Order Seq 4 (should be buffered)
	event4 := makeEvent(planID, orderID, "OrderShipped", 4)
	c.handleSequencedEvent(ctx, event4)

	// Verify event 4 is in buffer
	keys, err := c.redis.GetBufferKeys(ctx)
	require.NoError(t, err)
	found := false
	for _, k := range keys {
		if k == "buffer:"+planID {
			found = true
			break
		}
	}
	assert.True(t, found, "event 4 should be buffered")

	// 5. Process Seq 3 (should trigger drain of buffered seq 4)
	event3 := makeEvent(planID, orderID, "PaymentProcessed", 3)
	c.handleSequencedEvent(ctx, event3)
}

func TestConsumer_AbortFlow(t *testing.T) {
	c := setupConsumer(t)
	ctx := context.Background()

	planID := "plan-" + uuid.New().String()
	orderID := uuid.New().String()

	// Process first event
	event1 := makeEvent(planID, orderID, "OrderCreated", 1)
	c.handleSequencedEvent(ctx, event1)

	// Buffer an out-of-order event
	event3 := makeEvent(planID, orderID, "PaymentProcessed", 3)
	raw, _ := json.Marshal(event3)
	c.redis.BufferEvent(ctx, planID, 3, raw)

	// Abort the plan
	abortEvent := &models.EventEnvelope{
		EventID:   uuid.New().String(),
		EventType: "ABORT_PLAN",
		PlanID:    planID,
		OrderID:   orderID,
		Data:      map[string]any{"reason": "test-abort"},
	}
	c.handleAbort(ctx, abortEvent)

	// Verify buffer is cleared
	keys, _ := c.redis.GetBufferKeys(ctx)
	for _, k := range keys {
		assert.NotEqual(t, "buffer:"+planID, k, "buffer should be cleared after abort")
	}

	// Verify subsequent events are rejected (ABORTED)
	event2 := makeEvent(planID, orderID, "InventoryValidated", 2)
	c.handleSequencedEvent(ctx, event2)
	// No error expected — just silently discarded as ABORTED
}
