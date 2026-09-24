package db

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/user/nexus-server/internal/sequencing"
	"github.com/user/nexus-server/internal/testutil/pgtest"
	"github.com/user/nexus-server/pkg/models"
)

var eventTypes = []string{"OrderCreated", "InventoryValidated", "PaymentProcessed", "OrderShipped", "OrderCompleted"}

type plan struct {
	id      string
	orderID string
}

func newPlan() plan {
	return plan{id: "plan_" + uuid.NewString()[:8], orderID: uuid.NewString()}
}

func (p plan) event(seq int) *models.EventEnvelope {
	return &models.EventEnvelope{
		EventID:   uuid.NewString(),
		EventType: eventTypes[seq-1],
		PlanID:    p.id,
		SeqID:     &seq,
		OrderID:   p.orderID,
		Data:      map[string]any{"user_id": "usr_test", "total_amount": 100.0},
	}
}

func (p plan) abort() *models.EventEnvelope {
	return &models.EventEnvelope{
		EventID:   uuid.NewString(),
		EventType: models.EventTypeAbortPlan,
		PlanID:    p.id,
		OrderID:   p.orderID,
		Data:      map[string]any{"reason": "test", "abort_code": "manual"},
	}
}

func newRepo(t *testing.T) *Repository {
	t.Helper()
	repo, err := New(context.Background(), pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(repo.Close)
	return repo
}

func apply(t *testing.T, repo *Repository, e *models.EventEnvelope) Result {
	t.Helper()
	res, err := repo.ApplyEvent(context.Background(), e)
	require.NoError(t, err)
	return res
}

type orderRow struct {
	status  string
	lastSeq int
}

func readOrder(t *testing.T, repo *Repository, orderID string) orderRow {
	t.Helper()
	var o orderRow
	err := repo.pool.QueryRow(context.Background(),
		`SELECT status, last_seq_processed FROM orders WHERE id = $1::uuid`, orderID,
	).Scan(&o.status, &o.lastSeq)
	require.NoError(t, err)
	return o
}

// outboxTypes returns the order's outbox event types in insertion order.
func outboxTypes(t *testing.T, repo *Repository, orderID string) []string {
	t.Helper()
	rows, err := repo.pool.Query(context.Background(),
		`SELECT event_type FROM outbox WHERE aggregate_id = $1::uuid ORDER BY position`, orderID)
	require.NoError(t, err)
	defer rows.Close()
	var types []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		types = append(types, s)
	}
	require.NoError(t, rows.Err())
	return types
}

func countPending(t *testing.T, repo *Repository, orderID string) int {
	t.Helper()
	var n int
	require.NoError(t, repo.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pending_events WHERE order_id = $1::uuid`, orderID).Scan(&n))
	return n
}

func TestApplyEvent_InOrder(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()

	for seq := 1; seq <= 5; seq++ {
		res := apply(t, repo, p.event(seq))
		assert.Equal(t, sequencing.Apply, res.Outcome)
		assert.Equal(t, seq, res.LastSeq)
	}

	assert.Equal(t, orderRow{"completed", 5}, readOrder(t, repo, p.orderID))
	assert.Equal(t, eventTypes, outboxTypes(t, repo, p.orderID))
}

func TestApplyEvent_ReplayIsDuplicate(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()
	apply(t, repo, p.event(1))
	apply(t, repo, p.event(2))

	res := apply(t, repo, p.event(1))

	assert.Equal(t, sequencing.Duplicate, res.Outcome)
	assert.Equal(t, 2, res.LastSeq)
	assert.Len(t, outboxTypes(t, repo, p.orderID), 2, "a replay must not notify downstream again")
}

// Regression: seq 3 used to be accepted while seq 2 was missing (Redis had
// advanced past a failed Postgres write), silently skipping seq 2.
func TestApplyEvent_GapIsBufferedNeverSkipped(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()
	apply(t, repo, p.event(1))

	res := apply(t, repo, p.event(3))
	assert.Equal(t, sequencing.Buffer, res.Outcome)
	assert.Equal(t, orderRow{"pending", 1}, readOrder(t, repo, p.orderID))
	assert.Equal(t, 1, countPending(t, repo, p.orderID))

	res = apply(t, repo, p.event(2))
	assert.Equal(t, sequencing.Apply, res.Outcome)
	assert.Equal(t, 1, res.Drained)
	assert.Equal(t, 3, res.LastSeq)
	assert.Equal(t, orderRow{"payment_processed", 3}, readOrder(t, repo, p.orderID))
	assert.Equal(t, eventTypes[:3], outboxTypes(t, repo, p.orderID))
	assert.Zero(t, countPending(t, repo, p.orderID))
}

func TestApplyEvent_ReverseOrderDrainsInOneTransaction(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()

	for seq := 5; seq >= 2; seq-- {
		assert.Equal(t, sequencing.Buffer, apply(t, repo, p.event(seq)).Outcome)
	}
	// A redelivered out-of-order event stays buffered once.
	assert.Equal(t, sequencing.Buffer, apply(t, repo, p.event(4)).Outcome)
	assert.Equal(t, 4, countPending(t, repo, p.orderID))

	res := apply(t, repo, p.event(1))

	assert.Equal(t, 4, res.Drained)
	assert.Equal(t, 5, res.LastSeq)
	assert.Equal(t, orderRow{"completed", 5}, readOrder(t, repo, p.orderID))
	assert.Equal(t, eventTypes, outboxTypes(t, repo, p.orderID))
	assert.Zero(t, countPending(t, repo, p.orderID))
}

func TestApplyEvent_PlanMismatch(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()
	apply(t, repo, p.event(1))

	other := plan{id: "plan_other", orderID: p.orderID}
	res := apply(t, repo, other.event(2))

	assert.Equal(t, sequencing.PlanMismatch, res.Outcome)
	assert.Equal(t, orderRow{"pending", 1}, readOrder(t, repo, p.orderID))
}

func TestAbortPlan_InProgress(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	p := newPlan()
	apply(t, repo, p.event(1))
	apply(t, repo, p.event(3)) // buffered

	res, err := repo.AbortPlan(ctx, p.abort())
	require.NoError(t, err)
	assert.Equal(t, sequencing.Apply, res.Outcome)
	assert.Equal(t, "aborted", readOrder(t, repo, p.orderID).status)
	assert.Zero(t, countPending(t, repo, p.orderID), "buffered events of an aborted plan are discarded")

	// Redelivered abort: no second notification.
	res, err = repo.AbortPlan(ctx, p.abort())
	require.NoError(t, err)
	assert.Equal(t, sequencing.Duplicate, res.Outcome)
	assert.Equal(t, []string{"OrderCreated", "ABORT_PLAN"}, outboxTypes(t, repo, p.orderID))

	// Zombie events are discarded.
	assert.Equal(t, sequencing.Discard, apply(t, repo, p.event(2)).Outcome)
	assert.Equal(t, orderRow{"aborted", 1}, readOrder(t, repo, p.orderID))
}

func TestAbortPlan_BeforeAnyEventLeavesTombstone(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()
	apply(t, repo, p.event(2)) // buffered, order row does not exist yet

	res, err := repo.AbortPlan(context.Background(), p.abort())
	require.NoError(t, err)
	assert.Equal(t, sequencing.Tombstone, res.Outcome)
	assert.Zero(t, countPending(t, repo, p.orderID))
	assert.Empty(t, outboxTypes(t, repo, p.orderID), "nobody downstream knew about this order")

	assert.Equal(t, sequencing.Discard, apply(t, repo, p.event(1)).Outcome)
	assert.Equal(t, orderRow{"aborted", 0}, readOrder(t, repo, p.orderID))
}

func TestAbortPlan_CompletedOrderIsTerminal(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()
	for seq := 1; seq <= 5; seq++ {
		apply(t, repo, p.event(seq))
	}

	res, err := repo.AbortPlan(context.Background(), p.abort())
	require.NoError(t, err)
	assert.Equal(t, sequencing.Discard, res.Outcome)
	assert.Equal(t, orderRow{"completed", 5}, readOrder(t, repo, p.orderID))
	assert.Len(t, outboxTypes(t, repo, p.orderID), 5)
}

// Two consumers can briefly own the same partition during a rebalance. Every
// interleaving of concurrent, shuffled, repeated deliveries must converge to
// exactly one application of each event.
func TestApplyEvent_ConcurrentDeliveriesApplyEachEventOnce(t *testing.T) {
	repo := newRepo(t)
	p := newPlan()

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*5)
	for w := 0; w < workers; w++ {
		order := rand.Perm(5)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, i := range order {
				if _, err := repo.ApplyEvent(context.Background(), p.event(i+1)); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.Equal(t, orderRow{"completed", 5}, readOrder(t, repo, p.orderID))
	assert.Equal(t, eventTypes, outboxTypes(t, repo, p.orderID))
	assert.Zero(t, countPending(t, repo, p.orderID))
}

func TestBacklogCounts(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	p := newPlan()
	apply(t, repo, p.event(1))
	apply(t, repo, p.event(2))
	apply(t, repo, p.event(4)) // buffered

	pending, dead, err := repo.OutboxBacklog(ctx)
	require.NoError(t, err)
	assert.Equal(t, [2]int64{2, 0}, [2]int64{pending, dead})
	buffered, err := repo.PendingEventsCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), buffered)

	claimed, err := repo.ClaimOutbox(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "only the order's head is claimable")
	require.NoError(t, repo.MarkOutboxFailed(ctx, claimed[0].ID, 0, "rejected with status 400", true))
	claimed, err = repo.ClaimOutbox(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, repo.MarkOutboxDelivered(ctx, claimed[0].ID))

	pending, dead, err = repo.OutboxBacklog(ctx)
	require.NoError(t, err)
	assert.Equal(t, [2]int64{0, 1}, [2]int64{pending, dead}, "delivered entries leave the backlog; dead ones are counted apart")
}

func TestExpirePending(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	p := newPlan()
	apply(t, repo, p.event(1))
	apply(t, repo, p.event(3))
	apply(t, repo, p.event(4))
	_, err := repo.pool.Exec(ctx, `UPDATE pending_events SET received_at = NOW() - INTERVAL '2 hours' WHERE seq_id = 3 AND order_id = $1::uuid`, p.orderID)
	require.NoError(t, err)

	t.Run("sink failure keeps the event buffered", func(t *testing.T) {
		n, err := repo.ExpirePending(ctx, time.Hour, 10, func(context.Context, *models.EventEnvelope) error {
			return errors.New("dlq down")
		})
		assert.Error(t, err)
		assert.Zero(t, n)
		assert.Equal(t, 2, countPending(t, repo, p.orderID))
	})

	t.Run("expired event leaves the buffer after the sink accepts it", func(t *testing.T) {
		var got []int
		n, err := repo.ExpirePending(ctx, time.Hour, 10, func(_ context.Context, e *models.EventEnvelope) error {
			got = append(got, *e.SeqID)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Equal(t, []int{3}, got)
		assert.Equal(t, 1, countPending(t, repo, p.orderID), "events younger than maxAge stay")
	})
}

func TestIsPermanent(t *testing.T) {
	assert.True(t, IsPermanent(&pgconn.PgError{Code: "22P02"}), "invalid text representation")
	assert.True(t, IsPermanent(&pgconn.PgError{Code: "23514"}), "check violation")
	assert.False(t, IsPermanent(&pgconn.PgError{Code: "40001"}), "serialization failure")
	assert.False(t, IsPermanent(&pgconn.PgError{Code: "57P01"}), "admin shutdown")
	assert.False(t, IsPermanent(errors.New("connection reset by peer")))
	assert.False(t, IsPermanent(context.DeadlineExceeded))
}
