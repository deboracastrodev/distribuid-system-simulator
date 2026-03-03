package dispatcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
	"github.com/user/nexus-server/internal/consul"
	"github.com/user/nexus-server/internal/db"
)

type mockRepo struct {
	entries   []db.OutboxEntry
	processed []string
}

func (m *mockRepo) FetchUnprocessedOutbox(ctx context.Context, limit int) ([]db.OutboxEntry, error) {
	return m.entries, nil
}

func (m *mockRepo) MarkOutboxProcessed(ctx context.Context, id string) error {
	m.processed = append(m.processed, id)
	return nil
}

func TestDispatcher_CircuitBreaker_Integration(t *testing.T) {
	// 1. Setup mock server that fails
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// 2. Setup Dispatcher
	repo := &mockRepo{
		entries: []db.OutboxEntry{
			{ID: "1", AggregateID: "agg1", EventType: "test", Payload: []byte(`{}`)},
			{ID: "2", AggregateID: "agg2", EventType: "test", Payload: []byte(`{}`)},
			{ID: "3", AggregateID: "agg3", EventType: "test", Payload: []byte(`{}`)},
		},
	}

	// Use a KVWatcher (defaults: FailureThreshold 5)
	kv, _ := consul.NewKVWatcher("localhost:8500") 

	disp := New(repo, server.URL, 1*time.Second, 1, 1*time.Millisecond, kv)

	ctx := context.Background()

	// The default FailureThreshold is 5. Let's fail 5 times.
	for i := 0; i < 5; i++ {
		_ = disp.dispatch(ctx, repo.entries[0])
	}

	cb := disp.getCB()
	if cb.State() != gobreaker.StateOpen {
		t.Errorf("expected CB state Open after 5 failures (default threshold), got %s", cb.State())
	}

	// Next call should fail fast
	err := disp.dispatch(ctx, repo.entries[1])
	if err == nil || err.Error() == "" {
		t.Fatal("expected fail-fast error from open CB")
	}

	if failCount != 5 {
		t.Errorf("expected 5 server calls, got %d", failCount)
	}
}

