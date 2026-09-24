package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/user/nexus-server/internal/db"
	"github.com/user/nexus-server/internal/metrics"
	"github.com/user/nexus-server/internal/sequencing"
	"github.com/user/nexus-server/pkg/models"
)

// --- fakes ---

type fakeStore struct {
	apply   func(*models.EventEnvelope) (db.Result, error)
	abort   func(*models.EventEnvelope) (db.Result, error)
	applied int
}

func (s *fakeStore) ApplyEvent(_ context.Context, e *models.EventEnvelope) (db.Result, error) {
	s.applied++
	return s.apply(e)
}

func (s *fakeStore) AbortPlan(_ context.Context, e *models.EventEnvelope) (db.Result, error) {
	return s.abort(e)
}

func (s *fakeStore) ExpirePending(context.Context, time.Duration, int, func(context.Context, *models.EventEnvelope) error) (int, error) {
	return 0, nil
}

type fakeDLQ struct {
	err   error
	codes []string
}

func (d *fakeDLQ) Send(_ context.Context, _ *models.EventEnvelope, _, code string) error {
	if d.err != nil {
		return d.err
	}
	d.codes = append(d.codes, code)
	return nil
}

func (d *fakeDLQ) SendRaw(_ context.Context, _ []byte, _, code string) error {
	if d.err != nil {
		return d.err
	}
	d.codes = append(d.codes, code)
	return nil
}

// --- helpers ---

var eventTypes = []string{"OrderCreated", "InventoryValidated", "PaymentProcessed", "OrderShipped", "OrderCompleted"}

type plan struct{ id, orderID string }

func newPlan() plan { return plan{id: "plan_" + uuid.NewString()[:8], orderID: uuid.NewString()} }

func (p plan) record(t *testing.T, seq int) *kgo.Record {
	t.Helper()
	return p.recordOf(t, &models.EventEnvelope{
		EventID:   uuid.NewString(),
		EventType: eventTypes[seq-1],
		PlanID:    p.id,
		SeqID:     &seq,
		OrderID:   p.orderID,
		Data:      map[string]any{"user_id": "usr_test", "total_amount": 100.0},
	})
}

func (p plan) abortRecord(t *testing.T) *kgo.Record {
	t.Helper()
	return p.recordOf(t, &models.EventEnvelope{
		EventID:   uuid.NewString(),
		EventType: models.EventTypeAbortPlan,
		PlanID:    p.id,
		OrderID:   p.orderID,
		Data:      map[string]any{"reason": "test", "abort_code": "manual"},
	})
}

func (p plan) recordOf(t *testing.T, e *models.EventEnvelope) *kgo.Record {
	t.Helper()
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	return &kgo.Record{Topic: "orders", Value: raw}
}

func applied(lastSeq int) func(*models.EventEnvelope) (db.Result, error) {
	return func(*models.EventEnvelope) (db.Result, error) {
		return db.Result{Outcome: sequencing.Apply, LastSeq: lastSeq}, nil
	}
}

func newTestConsumer(store Store, dlq DeadLetter) *Consumer {
	c := newConsumer(store, dlq, time.Hour, metrics.NewForTest())
	c.retryBase = time.Millisecond
	c.retryMax = 5 * time.Millisecond
	return c
}

var errConnReset = errors.New("connection reset by peer")

func events(c *Consumer, kind, outcome string) float64 {
	return testutil.ToFloat64(c.metrics.EventsProcessed.WithLabelValues(kind, outcome))
}

func dlqCount(c *Consumer, code string) float64 {
	return testutil.ToFloat64(c.metrics.DLQMessages.WithLabelValues(code))
}

// --- settling rules: nil error = offset may be committed ---

func TestHandleRecord_TransientStoreErrorIsNotSettled(t *testing.T) {
	store := &fakeStore{apply: func(*models.EventEnvelope) (db.Result, error) { return db.Result{}, errConnReset }}
	dlq := &fakeDLQ{}
	c := newTestConsumer(store, dlq)

	err := c.handleRecord(context.Background(), newPlan().record(t, 2))

	assert.ErrorIs(t, err, errConnReset)
	assert.Empty(t, dlq.codes, "a transient failure is retried, not dead-lettered")
	assert.Zero(t, events(c, "sequenced", "apply"), "an unsettled event is not counted")
}

func TestHandleRecord_PermanentStoreErrorGoesToDLQ(t *testing.T) {
	store := &fakeStore{apply: func(*models.EventEnvelope) (db.Result, error) {
		return db.Result{}, &pgconn.PgError{Code: "22003", Message: "numeric field overflow"}
	}}
	dlq := &fakeDLQ{}
	c := newTestConsumer(store, dlq)

	require.NoError(t, c.handleRecord(context.Background(), newPlan().record(t, 1)))
	assert.Equal(t, []string{"DB_REJECTED"}, dlq.codes)
	assert.Equal(t, float64(1), dlqCount(c, "DB_REJECTED"))
}

func TestHandleRecord_UnacknowledgedDLQSendIsNotSettled(t *testing.T) {
	dlq := &fakeDLQ{err: errors.New("broker unavailable")}
	c := newTestConsumer(&fakeStore{}, dlq)

	err := c.handleRecord(context.Background(), &kgo.Record{Value: []byte("{not json")})

	assert.Error(t, err, "committing past an event the DLQ did not accept would lose it")
	assert.Zero(t, dlqCount(c, "PARSE_ERROR"), "only acknowledged dead letters are counted")
}

func TestHandleRecord_UnprocessableEventsGoToDLQ(t *testing.T) {
	p := newPlan()
	tests := []struct {
		name   string
		record *kgo.Record
		code   string
	}{
		{"malformed JSON", &kgo.Record{Value: []byte("{not json")}, "PARSE_ERROR"},
		{"missing seq_id", p.recordOf(t, &models.EventEnvelope{EventType: "OrderCreated", PlanID: p.id, OrderID: p.orderID}), "INVALID_EVENT"},
		{"order_id not a UUID", p.recordOf(t, &models.EventEnvelope{EventType: models.EventTypeAbortPlan, PlanID: p.id, OrderID: "o-1"}), "INVALID_EVENT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, dlq := &fakeStore{}, &fakeDLQ{}
			c := newTestConsumer(store, dlq)

			require.NoError(t, c.handleRecord(context.Background(), tt.record))
			assert.Equal(t, []string{tt.code}, dlq.codes)
			assert.Equal(t, float64(1), dlqCount(c, tt.code))
			assert.Zero(t, store.applied)
		})
	}
}

func TestHandleRecord_PlanMismatchGoesToDLQ(t *testing.T) {
	store := &fakeStore{apply: func(*models.EventEnvelope) (db.Result, error) {
		return db.Result{Outcome: sequencing.PlanMismatch}, nil
	}}
	dlq := &fakeDLQ{}
	c := newTestConsumer(store, dlq)

	require.NoError(t, c.handleRecord(context.Background(), newPlan().record(t, 2)))
	assert.Equal(t, []string{"PLAN_MISMATCH"}, dlq.codes)
	assert.Equal(t, float64(1), dlqCount(c, "PLAN_MISMATCH"))
	assert.Equal(t, float64(1), events(c, "sequenced", "plan_mismatch"))
}

func TestHandleRecord_AppliedEventCountsDrainedSuccessors(t *testing.T) {
	store := &fakeStore{apply: func(*models.EventEnvelope) (db.Result, error) {
		return db.Result{Outcome: sequencing.Apply, LastSeq: 4, Drained: 2}, nil
	}}
	c := newTestConsumer(store, &fakeDLQ{})

	require.NoError(t, c.handleRecord(context.Background(), newPlan().record(t, 2)))
	assert.Equal(t, float64(1), events(c, "sequenced", "apply"))
	assert.Equal(t, float64(2), testutil.ToFloat64(c.metrics.EventsDrained))
}

// --- retry loop ---

func TestProcessWithRetry_RetriesTransientFailureInPlace(t *testing.T) {
	failures := 2
	store := &fakeStore{apply: func(*models.EventEnvelope) (db.Result, error) {
		if failures > 0 {
			failures--
			return db.Result{}, errConnReset
		}
		return db.Result{Outcome: sequencing.Apply, LastSeq: 1}, nil
	}}
	dlq := &fakeDLQ{}
	c := newTestConsumer(store, dlq)

	require.NoError(t, c.processWithRetry(context.Background(), newPlan().record(t, 1)))
	assert.Equal(t, 3, store.applied)
	assert.Empty(t, dlq.codes)
	assert.Equal(t, float64(2), testutil.ToFloat64(c.metrics.ConsumerRetries))
	assert.Equal(t, float64(1), events(c, "sequenced", "apply"), "counted once, not per attempt")
	var h dto.Metric
	require.NoError(t, c.metrics.EventSettleSeconds.Write(&h))
	assert.Equal(t, uint64(1), h.GetHistogram().GetSampleCount())
}

func TestProcessWithRetry_StopsWhenContextIsCancelled(t *testing.T) {
	store := &fakeStore{apply: func(*models.EventEnvelope) (db.Result, error) { return db.Result{}, errConnReset }}
	c := newTestConsumer(store, &fakeDLQ{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := c.processWithRetry(ctx, newPlan().record(t, 1))
	assert.ErrorIs(t, err, context.DeadlineExceeded, "an unsettled record must not be reported as done")
}
