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
	entries []db.OutboxEntry
	processed []string
}

func (m *mockRepo) FetchUnprocessedOutbox(ctx context.Context, limit int) ([]db.OutboxEntry, error) {
	return m.entries, nil
}

func (m *mockRepo) MarkOutboxProcessed(ctx context.Context, id string) error {
	m.processed = append(m.processed, id)
	return nil
}

func TestDispatcher_CircuitBreaker(t *testing.T) {
	// 1. Setup mock server that fails
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// 2. Setup Dispatcher with low threshold
	repo := &mockRepo{
		entries: []db.OutboxEntry{
			{ID: "1", AggregateID: "agg1", EventType: "test", Payload: []byte(`{}`)},
			{ID: "2", AggregateID: "agg2", EventType: "test", Payload: []byte(`{}`)},
			{ID: "3", AggregateID: "agg3", EventType: "test", Payload: []byte(`{}`)},
		},
	}
	
	// Create watcher with custom config (mocking Consul KV effect)
	kv, _ := consul.NewKVWatcher("localhost:8500") // won't connect but has defaults
	// Overwrite config manually for test
	// (In a real test we'd have a mock KV but here we just want to test Dispatcher logic)
	
	disp := New(repo, server.URL, 1*time.Second, 1, 1*time.Millisecond, kv)
	
	// Override CB for deterministic test
	disp.cb = gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
		Name: "test",
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= 2
		},
	})

	ctx := context.Background()

	// First call -> Fail 1
	err := disp.dispatch(ctx, repo.entries[0])
	if err == nil {
		t.Error("expected error from failed webhook")
	}

	// Second call -> Fail 2 -> CB should open
	err = disp.dispatch(ctx, repo.entries[1])
	if err == nil {
		t.Error("expected error from failed webhook")
	}
	
	if disp.cb.State() != gobreaker.StateOpen {
		t.Errorf("expected CB state Open, got %s", disp.cb.State())
	}

	// Third call -> Should fail fast (CB Open)
	err = disp.dispatch(ctx, repo.entries[2])
	if err == nil || err.Error() == "" {
		t.Fatal("expected error from open CB")
	}
	
	if failCount != 2 {
		t.Errorf("expected 2 server calls, got %d", failCount)
	}
}
