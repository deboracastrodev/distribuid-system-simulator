package dispatcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	consulkv "github.com/user/nexus-server/internal/consul"
	"github.com/user/nexus-server/internal/db"
	"github.com/user/nexus-server/internal/testutil/pgtest"
)

// --- unit ---

func TestBackoff(t *testing.T) {
	base, maxDelay := time.Second, 10*time.Second
	for attempt, want := range map[int]time.Duration{
		1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 8 * time.Second, 5: 10 * time.Second, 30: 10 * time.Second,
	} {
		assert.Equal(t, want, backoff(attempt, base, maxDelay), "attempt %d", attempt)
	}
}

func TestRetryableStatus(t *testing.T) {
	for status, want := range map[int]bool{
		500: true, 502: true, 503: true, 408: true, 429: true,
		400: false, 404: false, 409: false, 422: false,
	} {
		assert.Equal(t, want, retryableStatus(status), "status %d", status)
	}
}

// --- integration (Postgres + HTTP receiver) ---

type staticCB consulkv.CBConfig

func (s staticCB) Config() consulkv.CBConfig { return consulkv.CBConfig(s) }

func defaultCB() staticCB { return staticCB(consulkv.DefaultCBConfig()) }

type request struct {
	key       string
	aggregate string
	position  int64
	attempt   int
}

// receiver is a webhook endpoint whose response is decided per request.
type receiver struct {
	mu       sync.Mutex
	requests []request
	decide   func(r request) int
}

func newReceiver(t *testing.T, decide func(r request) int) (*receiver, string) {
	t.Helper()
	rcv := &receiver{decide: decide}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pos, _ := strconv.ParseInt(r.Header.Get("X-Outbox-Position"), 10, 64)
		attempt, _ := strconv.Atoi(r.Header.Get("X-Delivery-Attempt"))
		req := request{
			key:       r.Header.Get("Idempotency-Key"),
			aggregate: r.Header.Get("X-Aggregate-ID"),
			position:  pos,
			attempt:   attempt,
		}
		rcv.mu.Lock()
		rcv.requests = append(rcv.requests, req)
		status := rcv.decide(req)
		rcv.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return rcv, srv.URL
}

func (r *receiver) all() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]request(nil), r.requests...)
}

func (r *receiver) keys(status func(request) bool) []string {
	var keys []string
	for _, req := range r.all() {
		if status(req) {
			keys = append(keys, req.key)
		}
	}
	return keys
}

func always(status int) func(request) int { return func(request) int { return status } }

type env struct {
	repo *db.Repository
	pool *pgxpool.Pool
}

func newEnv(t *testing.T) env {
	t.Helper()
	dsn := pgtest.DSN(t)
	repo, err := db.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(repo.Close)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return env{repo: repo, pool: pool}
}

// seed inserts n notifications for a new aggregate and returns their IDs in order.
func (e env) seed(t *testing.T, n int) (aggregate string, ids []string) {
	t.Helper()
	aggregate = uuid.NewString()
	for i := 1; i <= n; i++ {
		var id string
		require.NoError(t, e.pool.QueryRow(context.Background(), `
			INSERT INTO outbox (aggregate_id, event_type, payload, topic)
			VALUES ($1::uuid, 'OrderCreated', $2, 'order-notifications')
			RETURNING id::text
		`, aggregate, fmt.Sprintf(`{"seq_id": %d}`, i)).Scan(&id))
		ids = append(ids, id)
	}
	return aggregate, ids
}

type outboxRow struct {
	processed bool
	attempts  int
	dead      bool
	leased    bool
	lastError string
}

func (e env) row(t *testing.T, id string) outboxRow {
	t.Helper()
	var r outboxRow
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT processed, attempts, dead_at IS NOT NULL, lease_until IS NOT NULL, COALESCE(last_error, '')
		FROM outbox WHERE id = $1::uuid
	`, id).Scan(&r.processed, &r.attempts, &r.dead, &r.leased, &r.lastError))
	return r
}

func (e env) pending(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox WHERE NOT processed AND dead_at IS NULL`).Scan(&n))
	return n
}

func testConfig(url string) Config {
	return Config{
		WebhookURL:  url,
		Workers:     4,
		BatchSize:   50,
		MaxAttempts: 100,
		RetryBase:   30 * time.Millisecond,
		RetryMax:    200 * time.Millisecond,
		Lease:       5 * time.Second,
	}
}

// dispatchUntil runs batches until cond holds, failing the test after timeout.
func dispatchUntil(t *testing.T, d *Dispatcher, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		require.True(t, time.Now().Before(deadline), "condition not reached before timeout")
		_, err := d.dispatchBatch(context.Background())
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRequestCarriesIdempotencyKeyAndPosition(t *testing.T) {
	e := newEnv(t)
	rcv, url := newReceiver(t, always(200))
	agg, ids := e.seed(t, 1)
	d := New(e.repo, testConfig(url), defaultCB())

	delivered, err := d.dispatchBatch(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, delivered)
	reqs := rcv.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, ids[0], reqs[0].key, "Idempotency-Key is the outbox entry ID")
	assert.Equal(t, agg, reqs[0].aggregate)
	assert.Positive(t, reqs[0].position)
	assert.Equal(t, 1, reqs[0].attempt)
	assert.Equal(t, outboxRow{processed: true, attempts: 1}, e.row(t, ids[0]))
}

// Regression: a failed entry used to be skipped while the entries after it
// were delivered, so an order's notifications arrived as [2, 3, 4, 5, 1].
func TestFailedEntryHoldsBackOnlyItsSuccessors(t *testing.T) {
	e := newEnv(t)
	_, ids := e.seed(t, 3)
	failedOnce := false
	rcv, url := newReceiver(t, func(r request) int {
		if r.key == ids[0] && !failedOnce {
			failedOnce = true
			return 503
		}
		return 200
	})
	d := New(e.repo, testConfig(url), defaultCB())

	_, err := d.dispatchBatch(context.Background())
	require.NoError(t, err)
	_, err = d.dispatchBatch(context.Background())
	require.NoError(t, err)
	assert.Len(t, rcv.all(), 1, "while the head waits for its retry, nothing after it is sent")

	dispatchUntil(t, d, func() bool { return e.pending(t) == 0 })

	assert.Equal(t, []string{ids[0], ids[0], ids[1], ids[2]}, rcv.keys(func(request) bool { return true }))
	assert.Equal(t, 2, e.row(t, ids[0]).attempts)
}

// Regression: a failing entry used to be retried in place with sleeps,
// stalling every other order's notifications behind it.
func TestFailingOrderDoesNotBlockOthers(t *testing.T) {
	e := newEnv(t)
	failing, failingIDs := e.seed(t, 2)
	_, healthyIDs := e.seed(t, 3)
	rcv, url := newReceiver(t, func(r request) int {
		if r.aggregate == failing {
			return 503
		}
		return 200
	})
	d := New(e.repo, testConfig(url), staticCB{FailureThreshold: 1000, SuccessThreshold: 1, Timeout: time.Second, OpenDuration: time.Minute})

	dispatchUntil(t, d, func() bool {
		for _, id := range healthyIDs {
			if !e.row(t, id).processed {
				return false
			}
		}
		return true
	})

	for _, req := range rcv.all() {
		if req.aggregate == failing {
			assert.Equal(t, failingIDs[0], req.key, "only the failing order's head is attempted")
		}
	}
	assert.False(t, e.row(t, failingIDs[1]).processed)
}

// Regression: 4xx responses used to be retried like server errors.
func TestRejectedEntryIsDeadLetteredWithoutRetry(t *testing.T) {
	e := newEnv(t)
	_, ids := e.seed(t, 2)
	rcv, url := newReceiver(t, func(r request) int {
		if r.key == ids[0] {
			return 400
		}
		return 200
	})
	d := New(e.repo, testConfig(url), defaultCB())

	dispatchUntil(t, d, func() bool { return e.row(t, ids[1]).processed })

	rejected := e.row(t, ids[0])
	assert.True(t, rejected.dead)
	assert.Equal(t, 1, rejected.attempts)
	assert.Contains(t, rejected.lastError, "400")
	assert.Equal(t, []string{ids[0], ids[1]}, rcv.keys(func(request) bool { return true }),
		"one attempt for the rejected entry; the next one is then delivered")
}

func TestRetryableFailureIsDeadLetteredAfterMaxAttempts(t *testing.T) {
	e := newEnv(t)
	_, ids := e.seed(t, 1)
	rcv, url := newReceiver(t, always(503))
	cfg := testConfig(url)
	cfg.MaxAttempts = 3
	d := New(e.repo, cfg, defaultCB())

	dispatchUntil(t, d, func() bool { return e.row(t, ids[0]).dead })

	assert.Len(t, rcv.all(), 3)
	assert.Equal(t, 3, e.row(t, ids[0]).attempts)
}

func TestConcurrentDispatchersDeliverEachEntryOnceInOrder(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 10; i++ {
		e.seed(t, 5)
	}
	rcv, url := newReceiver(t, always(200))

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		d := New(e.repo, testConfig(url), defaultCB())
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Run(ctx)
		}()
	}
	deadline := time.Now().Add(15 * time.Second)
	for e.pending(t) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	require.Zero(t, e.pending(t))
	reqs := rcv.all()
	assert.Len(t, reqs, 50, "each entry delivered exactly once")
	lastPos := map[string]int64{}
	for _, r := range reqs {
		assert.Greater(t, r.position, lastPos[r.aggregate], "order %s delivered out of order", r.aggregate)
		lastPos[r.aggregate] = r.position
	}
}

func TestExpiredLeaseIsClaimedAgain(t *testing.T) {
	e := newEnv(t)
	_, ids := e.seed(t, 1)
	rcv, url := newReceiver(t, always(200))
	d := New(e.repo, testConfig(url), defaultCB())

	// A dispatcher claims the entry and dies before delivering it.
	claimed, err := e.repo.ClaimOutbox(context.Background(), 10, 200*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	_, err = d.dispatchBatch(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rcv.all(), "a leased entry is not claimed by another dispatcher")

	time.Sleep(250 * time.Millisecond)
	dispatchUntil(t, d, func() bool { return e.row(t, ids[0]).processed })
	assert.Len(t, rcv.all(), 1)
}

func TestOpenCircuitLeavesEntriesUntouched(t *testing.T) {
	e := newEnv(t)
	_, first := e.seed(t, 1)
	_, second := e.seed(t, 1)
	rcv, url := newReceiver(t, always(503))
	cfg := testConfig(url)
	cfg.Workers = 1
	d := New(e.repo, cfg, staticCB{FailureThreshold: 1, SuccessThreshold: 1, Timeout: time.Second, OpenDuration: time.Minute})

	_, err := d.dispatchBatch(context.Background()) // one failure opens the circuit
	require.NoError(t, err)
	_, err = d.dispatchBatch(context.Background()) // open: nothing is claimed
	require.NoError(t, err)

	assert.Len(t, rcv.all(), 1)
	a, b := e.row(t, first[0]), e.row(t, second[0])
	assert.Equal(t, 1, a.attempts+b.attempts, "only the request actually sent counts as an attempt")
	assert.False(t, a.leased || b.leased, "the entry skipped by the open circuit was released")
}

// cancelAfterResponse cancels the dispatch context once the response has
// arrived, like a shutdown signal landing right after a successful delivery.
type cancelAfterResponse struct {
	next   http.RoundTripper
	cancel context.CancelFunc
}

func (c cancelAfterResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	c.cancel()
	return resp, err
}

func TestDeliveryCompletedDuringShutdownIsRecorded(t *testing.T) {
	e := newEnv(t)
	_, ids := e.seed(t, 1)
	rcv, url := newReceiver(t, always(200))
	d := New(e.repo, testConfig(url), defaultCB())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.httpClient.Transport = cancelAfterResponse{next: http.DefaultTransport, cancel: cancel}

	delivered, err := d.dispatchBatch(ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, delivered)
	assert.Len(t, rcv.all(), 1)
	assert.Equal(t, outboxRow{processed: true, attempts: 1}, e.row(t, ids[0]),
		"a delivery that completed must not be released and sent again")
}

func TestShutdownReleasesInFlightEntry(t *testing.T) {
	e := newEnv(t)
	_, ids := e.seed(t, 1)
	_, url := newReceiver(t, func(request) int {
		time.Sleep(300 * time.Millisecond)
		return 200
	})
	d := New(e.repo, testConfig(url), defaultCB())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := d.dispatchBatch(ctx)
	require.NoError(t, err)

	r := e.row(t, ids[0])
	assert.Equal(t, outboxRow{}, r, "not delivered, no attempt counted, lease released")
}
