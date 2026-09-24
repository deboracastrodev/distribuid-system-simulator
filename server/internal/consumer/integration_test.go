package consumer

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/user/nexus-server/internal/db"
	"github.com/user/nexus-server/internal/testutil/pgtest"
	"github.com/user/nexus-server/pkg/models"
)

// These tests run the consumer against real Postgres. They skip when
// POSTGRES_DSN is not set.

type env struct {
	consumer *Consumer
	pool     *pgxpool.Pool
	dlq      *fakeDLQ
}

func newEnv(t *testing.T, wrap func(Store) Store) *env {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.DSN(t)

	repo, err := db.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(repo.Close)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var store Store = repo
	if wrap != nil {
		store = wrap(repo)
	}
	dlq := &fakeDLQ{}
	return &env{consumer: newTestConsumer(store, dlq), pool: pool, dlq: dlq}
}

func (e *env) process(t *testing.T, records ...*kgo.Record) {
	t.Helper()
	for _, r := range records {
		require.NoError(t, e.consumer.processWithRetry(context.Background(), r))
	}
}

func (e *env) order(t *testing.T, orderID string) (status string, lastSeq int) {
	t.Helper()
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT status, last_seq_processed FROM orders WHERE id = $1::uuid`, orderID).Scan(&status, &lastSeq))
	return status, lastSeq
}

func (e *env) outbox(t *testing.T, orderID string) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT event_type FROM outbox WHERE aggregate_id = $1::uuid ORDER BY position`, orderID)
	require.NoError(t, err)
	defer rows.Close()
	var types []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		types = append(types, s)
	}
	return types
}

// flakyStore fails the first ApplyEvent of one seq_id, like a Postgres restart
// in the middle of a plan.
type flakyStore struct {
	Store
	failSeq int
	failed  atomic.Bool
}

func (s *flakyStore) ApplyEvent(ctx context.Context, e *models.EventEnvelope) (db.Result, error) {
	if *e.SeqID == s.failSeq && s.failed.CompareAndSwap(false, true) {
		return db.Result{}, errConnReset
	}
	return s.Store.ApplyEvent(ctx, e)
}

// Regression for the original bug: Redis accepted seq 2, Postgres failed, seq 2
// went to the DLQ and seq 3 was then applied on top of seq 1.
func TestIntegration_TransientDBFailureDoesNotCreateGap(t *testing.T) {
	e := newEnv(t, func(s Store) Store { return &flakyStore{Store: s, failSeq: 2} })
	p := newPlan()

	for seq := 1; seq <= 5; seq++ {
		e.process(t, p.record(t, seq))
	}

	status, lastSeq := e.order(t, p.orderID)
	assert.Equal(t, "completed", status)
	assert.Equal(t, 5, lastSeq)
	assert.Equal(t, eventTypes, e.outbox(t, p.orderID), "every event applied exactly once, in order")
	assert.Empty(t, e.dlq.codes)
}

func TestIntegration_RedeliveryAfterCrashIsIdempotent(t *testing.T) {
	e := newEnv(t, nil)
	p := newPlan()

	// A crash before the commit makes Kafka redeliver the whole batch.
	for i := 0; i < 3; i++ {
		for seq := 1; seq <= 5; seq++ {
			e.process(t, p.record(t, seq))
		}
	}

	assert.Equal(t, eventTypes, e.outbox(t, p.orderID))
}

func TestIntegration_OutOfOrderAndZombies(t *testing.T) {
	e := newEnv(t, nil)
	completed, aborted := newPlan(), newPlan()

	e.process(t,
		completed.record(t, 1), completed.record(t, 3), completed.record(t, 5),
		aborted.record(t, 1), aborted.abortRecord(t),
		completed.record(t, 4), completed.record(t, 2),
		aborted.record(t, 2), aborted.record(t, 3), // zombies
	)

	status, lastSeq := e.order(t, completed.orderID)
	assert.Equal(t, "completed", status)
	assert.Equal(t, 5, lastSeq)
	assert.Equal(t, eventTypes, e.outbox(t, completed.orderID))

	status, lastSeq = e.order(t, aborted.orderID)
	assert.Equal(t, "aborted", status)
	assert.Equal(t, 1, lastSeq)
	assert.Equal(t, []string{"OrderCreated", "ABORT_PLAN"}, e.outbox(t, aborted.orderID))
}
